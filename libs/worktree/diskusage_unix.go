//go:build !windows

package worktree

import (
	"io/fs"
	"syscall"
)

// fileUsage is the space info's file occupies on disk, and, when the file has
// more than one link, its identity so the caller can count it once.
func fileUsage(info fs.FileInfo) (size int64, id fileID, linked bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.Size(), fileID{}, false
	}
	size = int64(st.Blocks) * 512
	if info.IsDir() || st.Nlink <= 1 {
		return size, fileID{}, false
	}
	return size, fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}
