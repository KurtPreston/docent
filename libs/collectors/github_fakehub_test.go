package collectors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/internal/testenv"
)

// fakeHub is a stateful fake GitHub for watch-scan tests. The test binary
// stands in for `gh`: TestMain hands control to serveFakeHub when the
// FAKEHUB_DIR variable is set, and it answers from a JSON state file the test
// rewrites between polls. Unlike fakeGH's canned replies, it evaluates the
// watch searches' date ranges, sort orders, result cap, and paging, so a test
// can check an incremental scan against a full scan of the same PRs.
type fakeHub struct {
	t     *testing.T
	dir   string
	State hubState
}

// hubState is the fake's whole world.
type hubState struct {
	PRs []hubPR
	// Cap is how many results search returns at most, like GitHub's 1000.
	Cap int
	// PageSize is how many results a search page holds.
	PageSize int
	// Shown is how many changed paths a search node includes.
	Shown int
	// FailSlices fails every search sorted by creation date.
	FailSlices bool
}

// hubPR is one PR. Unmatched means it no longer matches the watch query's
// other qualifiers, such as after a label change.
type hubPR struct {
	Number    int
	Author    string
	Paths     []string
	Created   time.Time
	Updated   time.Time
	Head      string
	Base      string
	Closed    bool
	Unmatched bool
	FailFiles bool
}

func TestMain(m *testing.M) {
	if dir := os.Getenv("FAKEHUB_DIR"); dir != "" {
		os.Exit(serveFakeHub(dir, os.Args[1:]))
	}
	os.Exit(testenv.Run(m))
}

// newFakeHub installs the test binary as `gh` ahead of the real one on PATH.
func newFakeHub(t *testing.T, state hubState) *fakeHub {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(dir, "gh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKEHUB_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := &fakeHub{t: t, dir: dir, State: state}
	h.sync()
	return h
}

// sync writes the state for the next calls to read.
func (h *fakeHub) sync() {
	h.t.Helper()
	b, err := json.Marshal(h.State)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "state.json"), b, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// pr returns the PR with the given number, for the test to change.
func (h *fakeHub) pr(n int) *hubPR {
	for i := range h.State.PRs {
		if h.State.PRs[i].Number == n {
			return &h.State.PRs[i]
		}
	}
	h.t.Fatalf("no PR %d", n)
	return nil
}

// calls returns every call's arguments so far, one line each.
func (h *fakeHub) calls() []string {
	b, _ := os.ReadFile(filepath.Join(h.dir, "calls.log"))
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// serveFakeHub answers one `gh` call and returns its exit code.
func serveFakeHub(dir string, args []string) int {
	logCall(dir, args)
	var st hubState
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		err = json.Unmarshal(b, &st)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakehub:", err)
		return 99
	}

	// Route by what the call asks for
	joined := strings.Join(args, " ")
	switch {
	case len(args) > 2 && args[0] == "search" && args[1] == "prs" && args[2] == "--author" && contains(args, "open"):
		return st.authored(args[3])
	case len(args) > 1 && args[0] == "search" && args[1] == "prs":
		fmt.Print("[]")
		return 0
	case strings.Contains(joined, "/files"):
		return st.files(joined)
	case strings.Contains(joined, "-F number="):
		return st.detail(args)
	case strings.Contains(joined, "-F k0="):
		return st.detailBatch(args)
	case strings.Contains(joined, "q=is:pr is:open"):
		return st.search(args)
	}
	fmt.Fprintln(os.Stderr, "fakehub: no route for:", joined)
	return 97
}

// logCall records a call's arguments, minus the GraphQL documents.
func logCall(dir string, args []string) {
	var keep []string
	for _, a := range args {
		if !strings.HasPrefix(a, "query=") {
			keep = append(keep, a)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, strings.Join(keep, " "))
}

// flagValue returns the value of a `-f name=value` or `-F name=value` field.
func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if (args[i] == "-f" || args[i] == "-F") && strings.HasPrefix(args[i+1], name+"=") {
			return strings.TrimPrefix(args[i+1], name+"=")
		}
	}
	return ""
}

// search evaluates a watch search: open, still matching, within any date
// range, in the requested order, capped and paged.
func (st hubState) search(args []string) int {
	var updatedFrom, createdTo time.Time
	order := "updated"
	for _, tok := range strings.Fields(flagValue(args, "q")) {
		switch {
		case strings.HasPrefix(tok, "updated:>="):
			updatedFrom = parseGitHubTime(strings.TrimPrefix(tok, "updated:>="))
		case strings.HasPrefix(tok, "created:<="):
			createdTo = parseGitHubTime(strings.TrimPrefix(tok, "created:<="))
		case tok == "sort:created-desc":
			order = "created"
		}
	}
	if order == "created" && st.FailSlices {
		fmt.Fprintln(os.Stderr, "fakehub: slice search failed")
		return 1
	}

	// Filter and order the PRs the search matches
	var hits []hubPR
	for _, p := range st.PRs {
		if p.Closed || p.Unmatched || p.Updated.Before(updatedFrom) || (!createdTo.IsZero() && p.Created.After(createdTo)) {
			continue
		}
		hits = append(hits, p)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i].Updated, hits[j].Updated
		if order == "created" {
			a, b = hits[i].Created, hits[j].Created
		}
		if !a.Equal(b) {
			return a.After(b)
		}
		return hits[i].Number > hits[j].Number
	})
	matched := len(hits)
	if st.Cap > 0 && len(hits) > st.Cap {
		hits = hits[:st.Cap]
	}

	// Write one page, or every page when paginating
	start, _ := strconv.Atoi(flagValue(args, "endCursor"))
	for {
		end := min(start+st.PageSize, len(hits))
		fmt.Print(st.page(hits[start:end], matched, end < len(hits), end))
		if end >= len(hits) || !contains(args, "--paginate") {
			return 0
		}
		start = end
	}
}

// page renders one search page.
func (st hubState) page(prs []hubPR, matched int, more bool, next int) string {
	nodes := make([]map[string]any, 0, len(prs))
	for _, p := range prs {
		files := []map[string]string{}
		for i, path := range p.Paths {
			if i < st.Shown {
				files = append(files, map[string]string{"path": path})
			}
		}
		nodes = append(nodes, map[string]any{
			"title": fmt.Sprintf("PR %d", p.Number), "url": hubURL(p.Number), "isDraft": false,
			"createdAt": p.Created.Format(time.RFC3339), "updatedAt": p.Updated.Format(time.RFC3339),
			"headRefOid": p.Head, "baseRefName": p.Base,
			"author":     map[string]string{"login": p.Author, "__typename": "User"},
			"assignees":  map[string]any{"nodes": []any{}},
			"repository": map[string]string{"nameWithOwner": "my-org/my-repo"},
			"files":      map[string]any{"totalCount": len(p.Paths), "nodes": files},
		})
	}
	b, _ := json.Marshal(map[string]any{"data": map[string]any{"search": map[string]any{
		"issueCount": matched,
		"pageInfo":   map[string]any{"hasNextPage": more, "endCursor": strconv.Itoa(next)},
		"nodes":      nodes,
	}}})
	return string(b)
}

// authored answers `gh search prs --author <login> --state open` with the
// login's open PRs, whatever the watch query's other qualifiers say.
func (st hubState) authored(login string) int {
	rows := []map[string]any{}
	for _, p := range st.PRs {
		if p.Author == login && !p.Closed {
			rows = append(rows, map[string]any{
				"title": fmt.Sprintf("PR %d", p.Number), "url": hubURL(p.Number), "state": "open", "isDraft": false,
				"createdAt": p.Created.Format(time.RFC3339), "updatedAt": p.Updated.Format(time.RFC3339),
				"author":     map[string]any{"login": p.Author},
				"repository": map[string]string{"nameWithOwner": "my-org/my-repo"},
			})
		}
	}
	b, _ := json.Marshal(rows)
	fmt.Print(string(b))
	return 0
}

// detail answers a PR detail lookup.
func (st hubState) detail(args []string) int {
	n, _ := strconv.Atoi(flagValue(args, "number"))
	pr := st.detailNode(n)
	if pr == nil {
		fmt.Fprintln(os.Stderr, "fakehub: no PR", n)
		return 1
	}
	b, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
	fmt.Print(string(b))
	return 0
}

// detailBatch answers a batched detail query, one alias per -F kN= number.
func (st hubState) detailBatch(args []string) int {
	data := map[string]any{}
	for i := 0; ; i++ {
		v := flagValue(args, fmt.Sprintf("k%d", i))
		if v == "" {
			break
		}
		n, _ := strconv.Atoi(v)
		data[fmt.Sprintf("p%d", i)] = map[string]any{"pullRequest": st.detailNode(n)}
	}
	b, _ := json.Marshal(map[string]any{"data": data})
	fmt.Print(string(b))
	return 0
}

// detailNode renders PR n as the detail query returns it, or nil if unknown.
func (st hubState) detailNode(n int) map[string]any {
	for _, p := range st.PRs {
		if p.Number != n {
			continue
		}
		state := "OPEN"
		if p.Closed {
			state = "CLOSED"
		}
		return map[string]any{
			"state": state, "isDraft": false, "mergeable": "MERGEABLE", "reviewDecision": "",
			"updatedAt": p.Updated.Format(time.RFC3339), "headRefName": "fix", "headRefOid": p.Head, "baseRefName": p.Base,
			"author":        map[string]string{"login": p.Author, "__typename": "User"},
			"commits":       map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}},
			"reviewThreads": map[string]any{"nodes": []any{}}, "reviews": map[string]any{"nodes": []any{}}, "timelineItems": map[string]any{"nodes": []any{}},
		}
	}
	return nil
}

// files answers a REST listing of a PR's changed files.
func (st hubState) files(joined string) int {
	for _, p := range st.PRs {
		if !strings.Contains(joined, fmt.Sprintf("/pulls/%d/files", p.Number)) {
			continue
		}
		if p.FailFiles {
			fmt.Fprintln(os.Stderr, "fakehub: listing failed")
			return 1
		}
		for _, path := range p.Paths {
			fmt.Println(path)
		}
		return 0
	}
	return 1
}

// hubURL is a fake PR's URL.
func hubURL(n int) string {
	return fmt.Sprintf("https://github.com/my-org/my-repo/pull/%d", n)
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
