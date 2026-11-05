package daemonconfig

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// DefaultWorktreeRetention is how long docent keeps an idle worktree when
// worktreeRetention is unset. Long enough to span a weekend between turns of
// the same work; short enough that daily automation branches do not pile up.
const DefaultWorktreeRetention = 72 * time.Hour

// DefaultWorktreeMaxDisk is the cap on docent's worktrees when worktreeMaxDisk
// is unset. A busy automation setup can make dozens of full checkouts with
// dependencies installed in a day, which fills a disk long before any of them
// reaches its retention.
const DefaultWorktreeMaxDisk int64 = 200 << 30

// WorktreeRetentionDuration parses WorktreeRetention. Zero means never remove;
// an unparseable value falls back to the default rather than disabling cleanup,
// since a typo should not quietly let the disk fill.
func (c DaemonConfig) WorktreeRetentionDuration() time.Duration {
	v := strings.TrimSpace(c.WorktreeRetention)
	switch strings.ToLower(v) {
	case "":
		return DefaultWorktreeRetention
	case "0", "off", "never":
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return DefaultWorktreeRetention
	}
	return d
}

// WorktreeMaxDiskBytes parses WorktreeMaxDisk. Zero means no cap; like the
// retention, an unparseable value falls back to the default.
func (c DaemonConfig) WorktreeMaxDiskBytes() int64 {
	v := strings.ToLower(strings.TrimSpace(c.WorktreeMaxDisk))
	switch v {
	case "":
		return DefaultWorktreeMaxDisk
	case "0", "off", "never":
		return 0
	}
	n, ok := parseByteSize(v)
	if !ok {
		return DefaultWorktreeMaxDisk
	}
	return n
}

// byteUnits maps a size suffix to its multiplier. Units are binary, as `du -h`
// and `df -h` print them, so "200GB" means 200 GiB.
var byteUnits = []struct {
	suffix string
	scale  float64
}{
	{"tib", 1 << 40}, {"tb", 1 << 40}, {"t", 1 << 40},
	{"gib", 1 << 30}, {"gb", 1 << 30}, {"g", 1 << 30},
	{"mib", 1 << 20}, {"mb", 1 << 20}, {"m", 1 << 20},
	{"kib", 1 << 10}, {"kb", 1 << 10}, {"k", 1 << 10},
	{"b", 1},
}

// parseByteSize reads a lowercased size such as "200gb", "1.5t" or "4096".
func parseByteSize(v string) (int64, bool) {
	// Split off the unit suffix, defaulting to bytes.
	scale := 1.0
	for _, u := range byteUnits {
		if strings.HasSuffix(v, u.suffix) {
			v, scale = strings.TrimSuffix(v, u.suffix), u.scale
			break
		}
	}

	// Parse the number and reject anything negative or out of range.
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || f <= 0 || f*scale >= math.MaxInt64 {
		return 0, false
	}
	return int64(f * scale), true
}
