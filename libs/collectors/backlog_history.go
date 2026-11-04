package collectors

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// backlogHistory holds per-run caches. Parsed tasks are keyed by blob hash and
// shared between snapshots, so cached values (including Sections) are
// read-only once stored.
type backlogHistory struct {
	cfg    backlogConfig
	opts   *CollectOpts
	prefix string
	blobs  map[string][]byte
	tasks  map[string]backlogSnapshotTask
	// parses and snapshots count work done, for tests and diagnostics.
	parses, snapshots int
}

type backlogRevision struct {
	hash, parent string
	author       string
	at           time.Time
}

// backlogSnapshotRef is one assembled snapshot retained for reuse.
type backlogSnapshotRef struct {
	revision string
	tasks    map[string]backlogSnapshotTask
}

func newBacklogHistory(cfg backlogConfig, opts *CollectOpts) *backlogHistory {
	return &backlogHistory{cfg: cfg, opts: opts, blobs: map[string][]byte{}, tasks: map[string]backlogSnapshotTask{}}
}

// CollectEvents reads committed snapshots only; state collection remains usable
// without Git. Commit time is recording evidence, not an action timestamp.
func (c BacklogCollector) CollectEvents(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	cfg, err := parseBacklogConfig(directive)
	if err != nil || !cfg.gitEvents {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if cfg.pullCommand != "" {
		if _, err := c.run(ctx, cfg, BacklogCommand{Name: "sh", Args: []string{"-c", cfg.pullCommand}}, opts); err != nil {
			return nil, fmt.Errorf("backlog: pull_command failed: %w", err)
		}
	}
	var since time.Time
	if opts != nil {
		since = opts.Since
	}
	return newBacklogHistory(cfg, opts).events(ctx, since, opts.windowEnd(c.Clock))
}

// events compares each in-window first-parent commit with its parent. A
// commit's snapshot is usually the previous in-window commit's parent, so only
// that one snapshot is retained rather than every whole-board map.
func (h *backlogHistory) events(ctx context.Context, since, until time.Time) ([]StatusItem, error) {
	revisions, err := h.revisions(ctx)
	if err != nil {
		return nil, err
	}
	var items []StatusItem
	var previous backlogSnapshotRef
	for i, rev := range revisions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if rev.at.Before(since) || rev.at.After(until) {
			continue
		}
		reportProgress(h.opts, DirectiveProgress{
			DirectiveID: h.cfg.directiveID, Status: "running", Detail: "reading task history",
			Completed: i, Total: len(revisions),
		})

		// Read the parent first so a missing parent reports the history error.
		before := map[string]backlogSnapshotTask{}
		if rev.parent != "" {
			before, err = h.snapshot(ctx, rev.parent)
			if err != nil {
				return nil, fmt.Errorf("backlog history: cannot compare parent of %s (restore missing or shallow history): %w", rev.hash, err)
			}
		}

		// Reuse the previous parent snapshot when it is this commit.
		after := previous.tasks
		if previous.revision != rev.hash || after == nil {
			if after, err = h.snapshot(ctx, rev.hash); err != nil {
				return nil, err
			}
		}
		previous = backlogSnapshotRef{revision: rev.parent, tasks: before}
		items = append(items, backlogRevisionEvents(h.cfg.directiveID, rev, backlogSnapshots{before, after})...)
	}
	return items, nil
}

func (h *backlogHistory) git(ctx context.Context, args []string, input string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...)
	cmd.Dir = h.cfg.board
	cmd.Stdin = strings.NewReader(input)
	out, err := runGit(ctx, cmd, h.opts, h.cfg.directiveID)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("backlog git %s: %w: %s", args[0], err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("backlog git %s: %w", args[0], err)
	}
	return out, nil
}

func (h *backlogHistory) revisions(ctx context.Context) ([]backlogRevision, error) {
	prefix, err := h.git(ctx, []string{"rev-parse", "--show-prefix"}, "")
	if err != nil {
		return nil, err
	}
	h.prefix = strings.TrimSuffix(string(prefix), "\n")
	// An unborn symbolic HEAD with no refs is a valid empty repository;
	// failures in populated or detached repositories must still be surfaced.
	head, err := h.git(ctx, []string{"rev-parse", "--verify", "--quiet", "HEAD"}, "")
	if err != nil {
		if _, symbolicErr := h.git(ctx, []string{"symbolic-ref", "-q", "HEAD"}, ""); symbolicErr != nil {
			return nil, err
		}
		refs, refsErr := h.git(ctx, []string{"for-each-ref", "--format=%(refname)"}, "")
		if refsErr != nil || len(bytes.TrimSpace(refs)) != 0 {
			return nil, err
		}
		return nil, nil
	}
	out, err := h.git(ctx, []string{"log", "--first-parent", "--format=%H%x00%P%x00%ct%x00%an <%ae>", strings.TrimSpace(string(head)), "--"}, "")
	if err != nil {
		return nil, err
	}
	var revisions []backlogRevision
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "\x00")
		if len(parts) != 4 {
			return nil, fmt.Errorf("backlog history: malformed commit metadata")
		}
		seconds, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("backlog history: invalid commit time: %w", err)
		}
		rev := backlogRevision{hash: parts[0], at: time.Unix(seconds, 0).UTC(), author: parts[3]}
		if parents := strings.Fields(parts[1]); len(parents) > 0 {
			rev.parent = parents[0]
		} else {
			// Git can hide shallow parents in traversal output. Read the
			// actual commit header rather than treating a boundary as creation.
			commit, err := h.git(ctx, []string{"cat-file", "commit", rev.hash}, "")
			if err != nil {
				return nil, err
			}
			for _, header := range strings.Split(strings.SplitN(string(commit), "\n\n", 2)[0], "\n") {
				if strings.HasPrefix(header, "parent ") {
					rev.parent = strings.TrimPrefix(header, "parent ")
					break
				}
			}
		}
		revisions = append(revisions, rev)
	}
	return revisions, nil
}
