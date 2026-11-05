//go:build windows

package worktree

import "io/fs"

// fileUsage is the file's apparent size. Windows exposes neither allocated
// blocks nor link identity through fs.FileInfo, so hard links count each time.
func fileUsage(info fs.FileInfo) (size int64, id fileID, linked bool) {
	return info.Size(), fileID{}, false
}
