package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Target kinds: the placements an agent can be given for a branch.
const (
	// TargetExisting is a directory that already has the branch checked out.
	TargetExisting = "existing"
	// TargetCreate adds a worktree to the developer's own project.
	TargetCreate = "create"
	// TargetIsolated is docent's own worktree, under the state directory.
	TargetIsolated = "isolated"
	// TargetInPlace checks the branch out in the developer's single checkout,
	// switching whatever they were on.
	TargetInPlace = "in_place"
)

// Target is one place an agent could run, as offered to the user.
type Target struct {
	// Kind is one of the Target* constants.
	Kind string
	// Dir is where the agent would run: resolved for an existing directory,
	// predicted for one that would be created.
	Dir string
	// Label is the whole of what the user reads. It names the actual directory,
	// because "isolated" and "your checkout" mean nothing next to a path you
	// recognize.
	Label string
	// Owned reports that picking this gives docent a directory of its own, and
	// with it the turn-boundary commit and the divergence guard.
	Owned bool
	// Default marks the one selected when nothing is chosen.
	Default bool
	// Disabled explains why this cannot be picked, or is empty when it can.
	// Offered-but-disabled rather than omitted: a missing option looks like a
	// bug, and the reason is usually something the user can clear.
	Disabled string
}

// Snapshot is one view of what is on disk, shared across the targets computed
// for a whole page.
//
// Discovery is a filesystem walk and each project's layout is a subprocess, so
// recomputing per lane would multiply both by the number of lanes on screen.
// Layouts are read on demand rather than up front, since a page typically asks
// about a handful of the repositories a code_home holds.
type Snapshot struct {
	stateRoot string
	projects  []Project
	byRepo    map[string]Project

	mu      sync.Mutex
	layouts map[string]Layout
	dirty   map[string]bool
}

// NewSnapshot discovers the developer's projects under roots. stateRoot
// overrides docent's state directory, for tests.
func NewSnapshot(roots []string, stateRoot string) *Snapshot {
	projects := UniqueByRepo(DiscoverProjects(roots))
	byRepo := make(map[string]Project, len(projects))
	for _, p := range projects {
		byRepo[strings.ToLower(p.Repo)] = p
	}
	return &Snapshot{
		stateRoot: stateRoot,
		projects:  projects,
		byRepo:    byRepo,
		layouts:   map[string]Layout{},
		dirty:     map[string]bool{},
	}
}

// Projects lists the discovered projects, one per repository.
func (s *Snapshot) Projects() []Project { return s.projects }

// Project returns the developer's own copy of repo.
func (s *Snapshot) Project(repo string) (Project, bool) {
	p, ok := s.byRepo[strings.ToLower(strings.Trim(strings.TrimSpace(repo), "/"))]
	return p, ok
}

// IsolatedDir is where docent's own worktree for a branch lives, whether or not
// it exists yet.
func (s *Snapshot) IsolatedDir(repo, branch string) string {
	return filepath.Join(stateRoot(s.stateRoot), projectsDir, SanitizePath(repo), SanitizePath(branch))
}

// Targets enumerates where an agent could run for a repository and branch.
//
// The list is never empty: docent's own worktree is always available, because it
// is the one placement that does not depend on the developer having anything on
// disk. It is also the default, since it is the only one where an agent cannot
// disturb something the developer is in the middle of.
func (s *Snapshot) Targets(ctx context.Context, repo, branch string) []Target {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	branch = strings.Trim(strings.TrimSpace(branch), "/")
	if repo == "" || branch == "" {
		return nil
	}

	var out []Target
	if p, ok := s.Project(repo); ok {
		out = append(out, s.developerTargets(ctx, p, branch)...)
	}
	return append(out, Target{
		Kind:    TargetIsolated,
		Dir:     s.IsolatedDir(repo, branch),
		Label:   "Use docent's isolated " + branch + " worktree",
		Owned:   true,
		Default: true,
	})
}

func (s *Snapshot) developerTargets(ctx context.Context, p Project, branch string) []Target {
	layout, err := s.layout(ctx, p)
	if err != nil {
		// The project is on disk but git will not describe it. Saying so beats
		// silently dropping the options the user expects to see.
		return []Target{{
			Kind:     TargetExisting,
			Dir:      p.Dir,
			Label:    "Use " + Display(p.Dir),
			Disabled: "git cannot read this project: " + err.Error(),
		}}
	}

	if dir, ok := layout.ByBranch[branch]; ok {
		return []Target{{
			Kind:  TargetExisting,
			Dir:   dir,
			Label: "Use " + Display(dir),
		}}
	}
	if p.Kind == KindWorktrees {
		dir := filepath.Join(p.Dir, SanitizePath(branch))
		t := Target{Kind: TargetCreate, Dir: dir, Label: "Create " + Display(dir)}
		if _, err := os.Stat(dir); err == nil {
			t.Disabled = Display(dir) + " already exists"
		}
		return []Target{t}
	}

	// An ordinary clone has one working tree, so running here means switching
	// the branch under whatever the developer has open.
	t := Target{
		Kind:  TargetInPlace,
		Dir:   p.Dir,
		Label: fmt.Sprintf("Use %s, checkout %s", Display(p.Dir), branch),
	}
	if dirty, err := s.isDirty(ctx, p); err != nil {
		t.Disabled = "git cannot read this checkout: " + err.Error()
	} else if dirty {
		t.Disabled = "uncommitted changes in " + Display(p.Dir)
	}
	return []Target{t}
}

func (s *Snapshot) layout(ctx context.Context, p Project) (Layout, error) {
	s.mu.Lock()
	l, ok := s.layouts[p.Dir]
	s.mu.Unlock()
	if ok {
		return l, nil
	}
	l, err := List(ctx, p.GitDir)
	if err != nil {
		return Layout{}, err
	}
	s.mu.Lock()
	s.layouts[p.Dir] = l
	s.mu.Unlock()
	return l, nil
}

func (s *Snapshot) isDirty(ctx context.Context, p Project) (bool, error) {
	s.mu.Lock()
	d, ok := s.dirty[p.Dir]
	s.mu.Unlock()
	if ok {
		return d, nil
	}
	d, err := IsDirty(ctx, p.Dir)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	s.dirty[p.Dir] = d
	s.mu.Unlock()
	return d, nil
}

// Display abbreviates a path for a label, so a user reads ~/Code/salsa rather
// than a home directory they already know they are in.
func Display(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}
