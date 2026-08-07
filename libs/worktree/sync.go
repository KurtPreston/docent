package worktree

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// syncFetchTimeout bounds the pre-turn fetches. Shorter than provisioning's,
// because this runs before every turn and an unreachable remote must degrade
// into "docent works from what it has" quickly rather than stall the agent.
const syncFetchTimeout = 2 * time.Minute

// SyncRequest asks what happened to a branch since docent last looked.
type SyncRequest struct {
	// Dir is docent's worktree. Fetches reach the shared repository through it.
	Dir string
	// Branch is the branch checked out in Dir.
	Branch string
}

// SyncResult describes how docent's branch stands against the developer's.
type SyncResult struct {
	// Behind and Ahead count commits on either side of the fork point, both
	// zero when there is nothing to compare against.
	Behind, Ahead int
	// FastForwarded reports that docent moved onto the developer's tip.
	FastForwarded bool
	// Diverged reports commits on both sides. docent refuses rather than
	// merging: reconciling two agents' work unattended is not its call.
	Diverged bool
	// Note explains anything the caller should pass on -- a fetch that failed,
	// a fast-forward declined because the tree was dirty. Empty when the
	// comparison was clean and uneventful.
	Note string
}

// Sync brings docent's copy of a branch up to date with the developer's.
//
// This is the half of the two-copy problem docent can solve on its own. The
// developer commits in their own checkout and pushes nowhere; without this the
// agent's next turn starts from a tree that quietly predates that work and
// produces a fork nobody asked for. remote.local points at the developer's git
// directory, so their commits are readable over the filesystem whether or not
// they have reached a forge.
//
// Fetch failures are not errors. An unreachable origin is routine -- a laptop
// off VPN, a forge down -- and refusing to run an agent over it would be worse
// than running one on slightly older objects.
func Sync(ctx context.Context, req SyncRequest) (SyncResult, error) {
	dir := strings.TrimSpace(req.Dir)
	branch := strings.TrimSpace(req.Branch)
	if dir == "" || branch == "" {
		return SyncResult{}, nil
	}

	var notes []string
	for _, remote := range []string{localRemote, "origin"} {
		if !hasRemote(ctx, dir, remote) {
			continue
		}
		if err := gitRun(ctx, dir, syncFetchTimeout, "fetch", "--quiet", remote); err != nil {
			notes = append(notes, fmt.Sprintf("could not fetch %s", remote))
		}
	}

	theirs := "refs/remotes/" + localRemote + "/" + branch
	if !hasRef(ctx, dir, theirs) {
		// No local copy of the repository, or the developer has never had this
		// branch. Either way there is nothing that could have diverged.
		return SyncResult{Note: strings.Join(notes, "; ")}, nil
	}

	behind, ahead, err := countRange(ctx, dir, theirs, "HEAD")
	if err != nil {
		return SyncResult{}, err
	}
	res := SyncResult{Behind: behind, Ahead: ahead}
	switch {
	case behind > 0 && ahead > 0:
		res.Diverged = true
	case behind > 0:
		if dirty, err := IsDirty(ctx, dir); err != nil {
			return SyncResult{}, err
		} else if dirty {
			// Merging over an agent's uncommitted edits is how work is lost.
			// The branch has not forked yet, so running the turn from behind is
			// recoverable in a way that this is not.
			notes = append(notes, fmt.Sprintf("%d commit(s) behind your copy, left alone because the tree is dirty", behind))
		} else if err := gitRun(ctx, dir, gitTimeout, "merge", "--ff-only", "--quiet", theirs); err != nil {
			notes = append(notes, fmt.Sprintf("%d commit(s) behind your copy, but the fast-forward failed", behind))
		} else {
			res.FastForwarded = true
		}
	}
	res.Note = strings.Join(notes, "; ")
	return res, nil
}

// CommitAll snapshots everything in the working tree, reporting whether there
// was anything to snapshot.
//
// Every turn in docent's own worktree ends here. The alternative -- leaving the
// tree dirty between turns -- puts the agent's work somewhere no git command the
// developer runs will show it, in a directory they have never opened. A commit
// makes it fetchable, which is the whole mechanism by which docent's work
// reaches them.
//
// --no-verify because this is a checkpoint, not a contribution. A pre-commit
// hook that reformats or rejects would either mangle a turn's output or strand
// it, and the developer runs the hooks when they turn this into a real commit.
func CommitAll(ctx context.Context, dir, message string) (bool, error) {
	dirty, err := IsDirty(ctx, dir)
	if err != nil || !dirty {
		return false, err
	}
	if err := gitRun(ctx, dir, gitTimeout, "add", "--all"); err != nil {
		return false, err
	}
	if err := gitRun(ctx, dir, gitTimeout, "commit", "--no-verify", "--quiet", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// IsDirty reports uncommitted changes, tracked or not.
func IsDirty(ctx context.Context, dir string) (bool, error) {
	out, err := gitOutput(ctx, dir, gitTimeout, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// countRange returns how many commits each side has that the other does not.
func countRange(ctx context.Context, dir, left, right string) (int, int, error) {
	out, err := gitOutput(ctx, dir, gitTimeout, "rev-list", "--left-right", "--count", left+"..."+right)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("worktree: cannot read %s...%s counts from %q", left, right, out)
	}
	var l, r int
	if _, err := fmt.Sscan(fields[0], &l); err != nil {
		return 0, 0, fmt.Errorf("worktree: cannot read %s...%s counts from %q", left, right, out)
	}
	if _, err := fmt.Sscan(fields[1], &r); err != nil {
		return 0, 0, fmt.Errorf("worktree: cannot read %s...%s counts from %q", left, right, out)
	}
	return l, r, nil
}

func hasRemote(ctx context.Context, dir, name string) bool {
	out, err := gitOutput(ctx, dir, gitTimeout, "config", "--get", "remote."+name+".url")
	return err == nil && strings.TrimSpace(out) != ""
}
