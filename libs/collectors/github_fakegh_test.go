package collectors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// fakeGHScript stands in for `gh`. It logs each call's arguments, then answers
// with the first rule whose pattern is a substring of them. A rule can create a
// marker file when it starts and wait (up to five seconds) for another marker
// before answering, which lets a test prove two calls overlap without timing.
const fakeGHScript = `#!/bin/sh
args="$*"
printf '%s\n' "$args" | head -n 1 >> "$FAKEGH_DIR/calls.log"
for r in "$FAKEGH_DIR"/rules/*; do
  pat=$(cat "$r/pattern")
  case "$args" in
    *"$pat"*)
      [ -f "$r/touch" ] && : > "$FAKEGH_DIR/$(cat "$r/touch")"
      if [ -f "$r/waitfor" ]; then
        w="$FAKEGH_DIR/$(cat "$r/waitfor")"; n=0
        while [ ! -f "$w" ]; do
          n=$((n+1)); [ $n -gt 100 ] && { echo "fakegh: timed out waiting for $w" >&2; exit 98; }
          sleep 0.05
        done
      fi
      cat "$r/out"
      exit "$(cat "$r/exit")"
      ;;
  esac
done
echo "fakegh: no rule for: $args" >&2
exit 97
`

// fakeGH is a fake `gh` on PATH for one test.
type fakeGH struct {
	t     *testing.T
	dir   string
	rules int
}

// ghRule is one canned reply. Pattern is matched as a substring of the joined
// arguments; rules are tried in the order they were added.
type ghRule struct {
	Pattern string
	Out     string
	Exit    int
	Touch   string
	WaitFor string
}

// newFakeGH installs the fake `gh` ahead of the real one on PATH.
func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(fakeGHScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKEGH_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeGH{t: t, dir: dir}
}

// on adds a rule.
func (f *fakeGH) on(r ghRule) {
	f.t.Helper()
	f.rules++
	dir := filepath.Join(f.dir, "rules", fmt.Sprintf("%03d", f.rules))
	files := map[string]string{"pattern": r.Pattern, "out": r.Out, "exit": fmt.Sprint(r.Exit)}
	if r.Touch != "" {
		files["touch"] = r.Touch
	}
	if r.WaitFor != "" {
		files["waitfor"] = r.WaitFor
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

// calls returns the first line of each call's arguments, in call order.
func (f *fakeGH) calls() []string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// countCalls counts calls whose arguments contain substr.
func (f *fakeGH) countCalls(substr string) int {
	n := 0
	for _, c := range f.calls() {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

// fixturePR is one open PR as the watch search reports it.
type fixturePR struct {
	Number     int
	Author     string
	Paths      []string
	TotalFiles int
	UpdatedAt  string
}

// url is the PR's address in the fixture repository.
func (p fixturePR) url() string {
	return fmt.Sprintf("https://github.com/my-org/my-repo/pull/%d", p.Number)
}

// watchPageJSON renders one page of the watch search reply.
func watchPageJSON(t *testing.T, prs ...fixturePR) string {
	t.Helper()
	nodes := make([]map[string]any, 0, len(prs))
	for _, p := range prs {
		files := make([]map[string]string, 0, len(p.Paths))
		for _, path := range p.Paths {
			files = append(files, map[string]string{"path": path})
		}
		total := p.TotalFiles
		if total == 0 {
			total = len(p.Paths)
		}
		updated := p.UpdatedAt
		if updated == "" {
			updated = "2026-01-02T00:00:00Z"
		}
		nodes = append(nodes, map[string]any{
			"title": fmt.Sprintf("PR %d", p.Number), "url": p.url(), "isDraft": false,
			"createdAt": "2026-01-01T00:00:00Z", "updatedAt": updated,
			"author":     map[string]string{"login": p.Author, "__typename": "User"},
			"assignees":  map[string]any{"nodes": []any{}},
			"repository": map[string]string{"nameWithOwner": "my-org/my-repo"},
			"files":      map[string]any{"totalCount": total, "nodes": files},
		})
	}
	page := map[string]any{"data": map[string]any{"search": map[string]any{
		"pageInfo": map[string]any{"hasNextPage": false},
		"nodes":    nodes,
	}}}
	b, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// prDetailJSON renders a passing, mergeable prDetailQuery reply.
func prDetailJSON(author string) string {
	return `{"data":{"repository":{"pullRequest":{"isDraft":false,"mergeable":"MERGEABLE",` +
		`"reviewDecision":"","updatedAt":"2026-01-02T00:00:00Z","headRefName":"fix","headRefOid":"abc123",` +
		`"author":{"login":"` + author + `","__typename":"User"},` +
		`"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]},` +
		`"reviewThreads":{"nodes":[]},"reviews":{"nodes":[]},"timelineItems":{"nodes":[]}}}}}`
}

// watchDirective follows PRs in the fixture repository that touch services/api.
func watchDirective() userdata.Directive {
	return userdata.Directive{
		ID: "gh", Name: "GitHub", Collector: "github", Enabled: true,
		Target: map[string]string{"username": "my-user"},
		PRQueries: []userdata.PRQuery{{
			Relation: "api", Qualifiers: "repo:my-org/my-repo draft:false", Watch: true,
			Paths: []string{"services/api"},
		}},
	}
}

// stateOpts collects involved-scope state at a fixed time.
func stateOpts() *CollectOpts {
	now := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	return &CollectOpts{Mode: ModeState, Scope: ScopeInvolved, Until: now}
}
