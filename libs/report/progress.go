package report

import (
	"strings"
	"sync"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

// passProgress merges directive progress from collection passes that run at
// the same time. Callers key progress by directive ID and keep the latest
// update, so without merging a directive collected in both passes would show
// "done" when its first pass finished and flip back to "running", and an error
// in one pass could be overwritten by the other's success.
//
// A directive in a single pass passes through untouched. One in several passes
// has each update's detail prefixed with its pass, stays "running" until its
// last pass finishes, and ends as "error" if any of its passes failed.
type passProgress struct {
	mu      sync.Mutex
	forward func(collectors.DirectiveProgress)
	pending map[string]int
	passes  map[string]int
	results map[string][]string
	failed  map[string]bool
}

// collectionPass is one registry pass: its mode and the collector types it is
// limited to (nil for every collector).
type collectionPass struct {
	mode collectors.Mode
	only []string
}

// newPassProgress counts, for each directive, how many of passes will collect
// it, using the same enabled/allowed/capability rules as Registry.Collect.
func newPassProgress(reg *collectors.Registry, directives []userdata.Directive, passes []collectionPass, forward func(collectors.DirectiveProgress)) *passProgress {
	p := &passProgress{
		forward: forward,
		pending: map[string]int{},
		passes:  map[string]int{},
		results: map[string][]string{},
		failed:  map[string]bool{},
	}
	for _, pass := range passes {
		for _, d := range directives {
			if !d.Enabled || !collectorListed(pass.only, d.Collector) {
				continue
			}
			state, events := reg.Capabilities(d.Collector)
			if (pass.mode == collectors.ModeState && state) || (pass.mode == collectors.ModeEvents && events) {
				p.pending[d.ID]++
				p.passes[d.ID]++
			}
		}
	}
	return p
}

// collectorListed reports whether name is allowed by an OnlyCollectorTypes
// list, where an empty list allows everything.
func collectorListed(only []string, name string) bool {
	if len(only) == 0 {
		return true
	}
	for _, o := range only {
		if o == name {
			return true
		}
	}
	return false
}

// callback returns the OnDirectiveUpdate hook for one pass.
func (p *passProgress) callback(mode collectors.Mode) func(collectors.DirectiveProgress) {
	return func(u collectors.DirectiveProgress) { p.update(mode, u) }
}

func (p *passProgress) update(mode collectors.Mode, u collectors.DirectiveProgress) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Directives collected by one pass need no merging
	if p.passes[u.DirectiveID] < 2 {
		p.forward(u)
		return
	}

	// Intermediate updates name their pass and keep the directive running
	label := string(mode) + ": " + u.Detail
	if u.Status == "running" {
		u.Detail = label
		p.forward(u)
		return
	}

	// A finished pass records its outcome; only the last one ends the row
	p.pending[u.DirectiveID]--
	p.results[u.DirectiveID] = append(p.results[u.DirectiveID], label)
	if u.Status == "error" {
		p.failed[u.DirectiveID] = true
	}
	u.Detail = strings.Join(p.results[u.DirectiveID], "; ")
	switch {
	case p.pending[u.DirectiveID] > 0:
		u.Status = "running"
	case p.failed[u.DirectiveID]:
		u.Status = "error"
	}
	p.forward(u)
}
