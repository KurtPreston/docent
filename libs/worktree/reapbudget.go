package worktree

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"time"
)

// budgetSweep is the disk-cap half of a sweep: the trees that survived the
// idle pass, and the cap they must fit under together.
type budgetSweep struct {
	trees    []*reapTree
	maxBytes int64
	now      time.Time
}

// evictOverBudget removes the least recently used trees until docent's
// worktrees fit in maxBytes, and records the usage left.
//
// Every surviving tree counts toward the total, kept and dirty ones included,
// since they occupy the disk all the same; only clean, unkept trees idle past
// the one-hour floor are removed. When those run out the sweep stops over
// budget rather than touch a tree that might be in use.
func evictOverBudget(ctx context.Context, b budgetSweep, res *ReapResult) {
	// Measure every tree, counting hard-linked files once across all of them.
	seen := map[fileID]bool{}
	sizes := make(map[*reapTree]int64, len(b.trees))
	var total int64
	for _, t := range b.trees {
		sizes[t] = diskUsage(ctx, t.dir, seen)
		total += sizes[t]
	}

	// Gather the trees eviction may consider, least recently used first.
	type candidate struct {
		tree *reapTree
		used time.Time
	}
	var candidates []candidate
	for _, t := range b.trees {
		if !t.kept && t.outcome != reapDirty {
			candidates = append(candidates, candidate{tree: t, used: lastUsed(ctx, t.dir)})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].used.Before(candidates[j].used) })

	// Remove the oldest until usage fits.
	for _, c := range candidates {
		if total <= b.maxBytes || ctx.Err() != nil {
			break
		}
		if reapOne(ctx, c.tree, minReapIdle, b.now, res) == reapRemoved {
			total -= sizes[c.tree]
			res.Evicted = append(res.Evicted, c.tree.dir)
		}
	}
	res.UsedBytes = total
}

// fileID identifies a file on disk, so hard links are counted once.
type fileID struct {
	dev, ino uint64
}

// diskUsage is roughly what `du` reports for dir: the space its files occupy,
// skipping hard links already in seen. Unreadable entries are skipped, since
// an estimate is all a budget needs.
func diskUsage(ctx context.Context, dir string, seen map[fileID]bool) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		size, id, linked := fileUsage(info)
		if linked {
			if seen[id] {
				return nil
			}
			seen[id] = true
		}
		total += size
		return nil
	})
	return total
}
