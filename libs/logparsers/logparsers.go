// Package logparsers turns CI log text into test failures. Each parser
// understands one test runner's console output; collectors that read CI logs
// (e.g. azure-pipelines) pick one by name from directive config.
package logparsers

import (
	"sort"
	"strings"
)

// Test identifies one test. File groups tests (one signal per file) and Name
// is how the runner prints the test, unique within a run.
type Test struct {
	File string
	Name string
}

// Result is what one log says went wrong.
type Result struct {
	// Failed tests failed every attempt; Flaky tests failed and then passed
	// on a retry.
	Failed []Test
	Flaky  []Test
	// Errors maps Test.Name to the first error reported for it.
	Errors map[string]string
	// Complete is true when the log carried the runner's end-of-run summary.
	// Without it (a run killed by a job timeout, say) the result is
	// reconstructed from per-test lines and may miss tests.
	Complete bool
}

// Empty reports whether nothing failed or flaked.
func (r Result) Empty() bool {
	return len(r.Failed) == 0 && len(r.Flaky) == 0
}

// Merge folds o into r, for runs whose tests are split across several logs.
// Start from Result{Complete: true}; the merge stays complete only if every
// part was.
func (r *Result) Merge(o Result) {
	if r.Errors == nil {
		r.Errors = map[string]string{}
	}
	r.Failed = append(r.Failed, o.Failed...)
	r.Flaky = append(r.Flaky, o.Flaky...)
	r.Complete = r.Complete && o.Complete
	for k, v := range o.Errors {
		if _, ok := r.Errors[k]; !ok {
			r.Errors[k] = v
		}
	}
}

// Parser parses one log. Logs are plain console text with any CI-specific
// line prefixes (timestamps) already removed.
type Parser func(log string) Result

var registry = map[string]Parser{
	"playwright": ParsePlaywright,
}

// Get returns the parser registered under name.
func Get(name string) (Parser, bool) {
	p, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// Names lists the registered parsers, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// FileResult is the part of a Result that belongs to one test file.
type FileResult struct {
	File   string
	Failed []Test
	Flaky  []Test
}

// ByFile groups failed and flaky tests by file, sorted by file.
func (r Result) ByFile() []FileResult {
	byFile := map[string]*FileResult{}
	get := func(file string) *FileResult {
		if g, ok := byFile[file]; ok {
			return g
		}
		g := &FileResult{File: file}
		byFile[file] = g
		return g
	}
	for _, t := range r.Failed {
		g := get(t.File)
		g.Failed = append(g.Failed, t)
	}
	for _, t := range r.Flaky {
		g := get(t.File)
		g.Flaky = append(g.Flaky, t)
	}

	// Stable order keeps signal emission deterministic
	out := make([]FileResult, 0, len(byFile))
	for _, g := range byFile {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// Truncate shortens s to at most n runes, marking the cut.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n…(truncated)"
}
