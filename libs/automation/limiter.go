package automation

import "sync"

// ruleLimiter caps how many jobs of one rule run at once. Jobs beyond the cap
// wait in FIFO order rather than being dropped. It is keyed by rule ID, so the
// count survives a config reload that replaces the rule list.
type ruleLimiter struct {
	mu      sync.Mutex
	running map[string]int
	waiting map[string][]chan struct{}
}

// acquire takes a slot for ruleID when fewer than max jobs run. It returns nil
// when the slot is taken now, or a channel that is closed when a finishing job
// hands its slot to this one. Every acquire must be paired with one release,
// after the job runs.
func (l *ruleLimiter) acquire(ruleID string, max int) <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running == nil {
		l.running = map[string]int{}
		l.waiting = map[string][]chan struct{}{}
	}

	// Run now when a slot is free and nobody is ahead in line
	if l.running[ruleID] < max && len(l.waiting[ruleID]) == 0 {
		l.running[ruleID]++
		return nil
	}

	// Otherwise wait at the back of the rule's line
	ch := make(chan struct{})
	l.waiting[ruleID] = append(l.waiting[ruleID], ch)
	return ch
}

// release frees a slot for ruleID, handing it straight to the longest-waiting
// job when there is one.
func (l *ruleLimiter) release(ruleID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if q := l.waiting[ruleID]; len(q) > 0 {
		next := q[0]
		l.waiting[ruleID] = q[1:]
		close(next)
		return
	}
	if l.running[ruleID] > 0 {
		l.running[ruleID]--
	}
}
