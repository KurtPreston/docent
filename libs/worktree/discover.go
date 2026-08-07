package worktree

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KurtPreston/docent/libs/model"
)

// Kind names a project's on-disk layout.
const (
	// KindWorktrees is a directory whose children are working trees, with the
	// repository in a bare git directory beside them.
	KindWorktrees = "worktrees"
	// KindSingle is an ordinary clone with one working tree of its own.
	KindSingle = "single"
)

// gitName is the conventional name of a repository's git directory inside a
// working tree. Unlike a bare directory's name, this one is git's own and not a
// convention docent is free to ignore.
const gitName = ".git"

// Project is a repository as it exists on this machine.
type Project struct {
	// Dir is the project root: the directory worktrees live in for
	// KindWorktrees, or the checkout itself for KindSingle.
	Dir string
	// GitDir is the repository's git directory -- the bare child for
	// KindWorktrees, Dir/.git for KindSingle. Refs, config and origin all come
	// from here, so nothing needs to guess at a directory name.
	GitDir string
	// Kind is KindWorktrees or KindSingle.
	Kind string
	// Repo is the host-relative repository identity ("Chip/salsa"), or "" when
	// origin is absent or unparseable.
	Repo string
}

// Identify classifies dir, following a linked worktree back to the repository it
// belongs to.
//
// A bare child wins over dir's own .git, and the order matters more than it
// looks: a worktree project's root can itself be an unrelated repository -- a
// checkout of the scripts that set the project up, say, tracked separately from
// the code. Judging dir by its .git first would resolve the wrong git directory,
// read the wrong origin (or none), and leave the real project undiscovered.
func Identify(dir string) (Project, bool) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return Project{}, false
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if bare, ok := bareChild(dir); ok {
		return newProject(dir, bare, KindWorktrees), true
	}
	gitPath := filepath.Join(dir, gitName)
	switch {
	case isDir(gitPath):
		return newProject(dir, gitPath, KindSingle), true
	case isFile(gitPath):
		// A linked worktree is not a project; the repository it belongs to is.
		// Normalizing here is what stops every worktree of a project from being
		// listed beside the project itself.
		common, ok := commonDir(gitPath)
		if !ok {
			return Project{}, false
		}
		return fromGitDir(common)
	}
	return Project{}, false
}

// FindProject walks up from start to the project containing it, so a path docent
// already knows -- a worktree from local-git, a session's directory -- can be
// turned into a project. Evidence about a specific work item beats inferring a
// project from a repository name, so callers try this first.
func FindProject(start string) (Project, bool) {
	dir := strings.TrimSpace(start)
	if dir == "" {
		return Project{}, false
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for {
		if p, ok := Identify(dir); ok {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Project{}, false
		}
		dir = parent
	}
}

// DiscoverProjects finds the projects at or immediately below each root.
//
// Both levels are checked because the two shapes appear together in a normal
// setup: ~/Code/salsa is itself a project whose children are worktrees, while
// ~/Code is a directory of sibling projects. Results are sorted by path so
// repeated calls agree.
//
// The search stops one level down on purpose. A deeper walk would descend into
// every worktree of every project -- thousands of directories -- to find
// projects it has already found, on a path that runs while a caller waits.
// Children that normalize to a project already seen collapse into it, which is
// what keeps a project's own worktrees from being listed as peers.
func DiscoverProjects(roots []string) []Project {
	seen := map[string]bool{}
	var out []Project
	add := func(dir string) {
		p, ok := Identify(dir)
		if !ok || seen[p.Dir] {
			return
		}
		seen[p.Dir] = true
		out = append(out, p)
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		add(root)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(root, e.Name()))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// ProjectForRepo picks the project tracking repo from the given roots.
//
// One repository can legitimately be cloned twice (a ~/Code/salsa and a
// ~/Code/salsa2 for a long-running experiment), and an agent must not land in
// whichever one os.ReadDir happened to return first. The tie is broken toward
// the project whose directory name matches the repository name, then the
// shortest path, then lexically -- which picks salsa over salsa2 and stays
// stable across runs.
func ProjectForRepo(roots []string, repo string) (Project, bool) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if repo == "" {
		return Project{}, false
	}
	var matches []Project
	for _, p := range DiscoverProjects(roots) {
		if strings.EqualFold(p.Repo, repo) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return Project{}, false
	}
	best := matches[0]
	for _, m := range matches[1:] {
		if better(m.Dir, best.Dir, repoName(repo)) {
			best = m
		}
	}
	return best, true
}

// UniqueByRepo collapses projects to one per repository, keeping the same one
// ProjectForRepo would resolve to.
//
// Offering a caller a choice between two clones of the same repository is a
// false choice: a request names a repository, and resolution picks a project
// from that deterministically, so both options would do the identical thing.
// Projects with no usable origin are dropped, since nothing docent collects can
// be joined to them.
func UniqueByRepo(projects []Project) []Project {
	byRepo := map[string]Project{}
	var order []string
	for _, p := range projects {
		key := strings.ToLower(p.Repo)
		if key == "" {
			continue
		}
		cur, seen := byRepo[key]
		if !seen {
			byRepo[key], order = p, append(order, key)
			continue
		}
		if better(p.Dir, cur.Dir, repoName(p.Repo)) {
			byRepo[key] = p
		}
	}
	out := make([]Project, 0, len(order))
	for _, key := range order {
		out = append(out, byRepo[key])
	}
	return out
}

func newProject(dir, gitDir, kind string) Project {
	return Project{
		Dir:    filepath.Clean(dir),
		GitDir: filepath.Clean(gitDir),
		Kind:   kind,
		Repo:   model.RepoKeyFromRemote(OriginURL(gitDir)),
	}
}

// OriginURL reads remote.origin.url straight out of a git directory's config.
//
// Reading the file rather than shelling out to `git remote get-url` keeps
// discovery free of subprocesses, which is what makes it affordable to run over
// every repository under a code_home rather than only the handful that match a
// naming convention.
func OriginURL(gitDir string) string {
	return gitConfig(gitDir)["remote.origin.url"]
}

// fromGitDir builds the project owning a repository's common git directory.
func fromGitDir(gitDir string) (Project, bool) {
	parent := filepath.Dir(gitDir)
	if filepath.Base(gitDir) == gitName {
		return newProject(parent, gitDir, KindSingle), true
	}
	if isBareRepo(gitDir) {
		return newProject(parent, gitDir, KindWorktrees), true
	}
	return Project{}, false
}

// bareChild returns dir's first bare repository child, in lexical order so the
// answer is stable.
func bareChild(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == gitName {
			continue
		}
		cand := filepath.Join(dir, e.Name())
		if isBareRepo(cand) {
			return cand, true
		}
	}
	return "", false
}

// isBareRepo reports whether dir is a bare git repository.
//
// The HEAD check comes first because it fails for almost every directory ever
// passed here, making the common case one stat. core.bare is what separates a
// bare repository from the .git directory of an ordinary clone, which has the
// same HEAD, objects and refs.
func isBareRepo(dir string) bool {
	if !isFile(filepath.Join(dir, "HEAD")) {
		return false
	}
	if !isDir(filepath.Join(dir, "objects")) || !isDir(filepath.Join(dir, "refs")) {
		return false
	}
	return gitConfig(dir)["core.bare"] == "true"
}

// commonDir resolves a linked worktree's .git file to the repository's shared
// git directory.
//
// The file points at <common>/worktrees/<name>, which also holds a `commondir`
// file naming the shared directory outright; that is preferred over inferring it
// from the path shape. A .git file that points somewhere else -- a submodule's
// gitdir under modules/, say -- belongs to no worktree, and reporting that
// honestly keeps submodules from being mistaken for projects.
func commonDir(gitFile string) (string, bool) {
	b, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return "", false
	}
	p := strings.TrimSpace(rest)
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(gitFile), p)
	}
	p = filepath.Clean(p)
	if filepath.Base(filepath.Dir(p)) != "worktrees" {
		return "", false
	}
	if b, err := os.ReadFile(filepath.Join(p, "commondir")); err == nil {
		if rel := strings.TrimSpace(string(b)); rel != "" {
			if !filepath.IsAbs(rel) {
				rel = filepath.Join(p, rel)
			}
			return filepath.Clean(rel), true
		}
	}
	return filepath.Dir(filepath.Dir(p)), true
}

// gitConfig parses a git directory's own config file into "section.key" or
// "section.subsection.key" entries. It is deliberately not a full
// implementation: it answers core.bare and remote.origin.url, the two questions
// discovery has, without a subprocess. Includes are not followed, which is
// acceptable for values git itself writes into the file it created.
func gitConfig(gitDir string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return out
	}
	prefix := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			prefix = sectionPrefix(line[1 : len(line)-1])
			continue
		}
		if prefix == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if key != "" {
			out[prefix+key] = value
		}
	}
	return out
}

// sectionPrefix turns a section header's body into a dotted key prefix:
// `core` -> "core.", `remote "origin"` -> "remote.origin.".
func sectionPrefix(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	name, sub, hasSub := strings.Cut(body, " ")
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	if !hasSub {
		return name + "."
	}
	sub = strings.Trim(strings.TrimSpace(sub), `"`)
	if sub == "" {
		return name + "."
	}
	return name + "." + sub + "."
}

// repoName is the trailing segment of an owner/repo identity, which is what a
// project directory is usually named after.
func repoName(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

func better(candidate, current, repoName string) bool {
	cExact := strings.EqualFold(filepath.Base(candidate), repoName)
	bExact := strings.EqualFold(filepath.Base(current), repoName)
	if cExact != bExact {
		return cExact
	}
	if len(candidate) != len(current) {
		return len(candidate) < len(current)
	}
	return candidate < current
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
