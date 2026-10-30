package collectors

import (
	"regexp"
	"sort"
	"strings"
)

// PlaywrightTest identifies one Playwright test as the list reporter prints
// it: "[chromium] › e2e/foo.spec.ts:12:3 › does a thing".
type PlaywrightTest struct {
	Project string
	File    string
	Line    string
	Title   string
}

// Name renders the test the way Playwright prints it, minus the project.
func (t PlaywrightTest) Name() string {
	return t.File + ":" + t.Line + " › " + t.Title
}

// PlaywrightResult is what a CI log says went wrong in one Playwright run.
type PlaywrightResult struct {
	// Failed tests failed every attempt; Flaky tests failed and then passed
	// on a retry.
	Failed []PlaywrightTest
	Flaky  []PlaywrightTest
	// Errors maps PlaywrightTest.Name() to the first error Playwright
	// reported for it, decoded from its `::error` annotation.
	Errors map[string]string
	// Summarized is true when the log carried Playwright's end-of-run
	// summary. Without it (a run killed by a job timeout, say) the result is
	// reconstructed from the per-test lines and may be incomplete.
	Summarized bool
}

// Empty reports whether the run had nothing failed or flaky.
func (r PlaywrightResult) Empty() bool {
	return len(r.Failed) == 0 && len(r.Flaky) == 0
}

var (
	// "2026-10-29T23:11:38.0427473Z " prefixes every Azure Pipelines log line.
	adoTimestampRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z ?`)
	// "  5 failed", "  1 flaky", "  195 passed (7.9m)" in the final summary.
	pwSummaryCountRE = regexp.MustCompile(`^  (\d+) (failed|flaky|passed|skipped|interrupted|did not run)\b`)
	// "    [chromium] › e2e/foo.spec.ts:12:3 › title" listed under a count.
	pwSummaryTestRE = regexp.MustCompile(`^    (\[[^\]]+\] › .+)$`)
	// "  ✘   46 [chromium] › e2e/foo.spec.ts:12:3 › title (retry #1) (13.1s)"
	pwResultLineRE = regexp.MustCompile(`^\s*([✓✘-])\s+\d+\s+(\[[^\]]+\] › .*?)(?: \(retry #\d+\))?(?: \([\d.]+(?:ms|s|m|h)\))?(?:::|$)`)
	// "[chromium] › e2e/foo.spec.ts:12:3 › title"
	pwTestRE = regexp.MustCompile(`^\[([^\]]+)\] › (.+?):(\d+):(\d+) › (.+?)\s*$`)
	// "::error file=…,title=<test>,line=…::<urlencoded message>"
	pwErrorAnnotationRE = regexp.MustCompile(`::error (?:[^:]*?,)?title=(.+?)(?:,line=\d+)?(?:,col=\d+)?::(.*)$`)
)

// maxPlaywrightErrorLen caps each decoded error so one enormous call log
// can't crowd out the rest of a signal.
const maxPlaywrightErrorLen = 2500

// ParsePlaywrightLog extracts failed and flaky tests from the output of
// Playwright's list (or line) reporter as captured in a CI log.
func ParsePlaywrightLog(log string) PlaywrightResult {
	res := PlaywrightResult{Errors: map[string]string{}}
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")

	// Per-test outcomes from the result lines, used when no summary exists
	lineFailed := map[string]PlaywrightTest{}
	linePassed := map[string]bool{}
	var lineOrder []string

	// Walk the log once, collecting summary sections, result lines, and errors
	section := ""
	var summaryFailed, summaryFlaky []PlaywrightTest
	for _, raw := range lines {
		line := adoTimestampRE.ReplaceAllString(raw, "")

		// The final summary lists each failed/flaky test under its count line
		if m := pwSummaryCountRE.FindStringSubmatch(line); m != nil {
			section = m[2]
			if section == "failed" || section == "flaky" {
				res.Summarized = true
				// A later summary (another reporter pass) supersedes an earlier one
				if section == "failed" {
					summaryFailed = nil
				} else {
					summaryFlaky = nil
				}
			}
			continue
		}
		if m := pwSummaryTestRE.FindStringSubmatch(line); m != nil && (section == "failed" || section == "flaky") {
			if t, ok := parsePlaywrightTest(m[1]); ok {
				if section == "failed" {
					summaryFailed = append(summaryFailed, t)
				} else {
					summaryFlaky = append(summaryFlaky, t)
				}
			}
			continue
		}
		if strings.TrimSpace(line) != "" {
			section = ""
		}

		// Per-test result lines: ✘ for a failed attempt, ✓ for a pass
		if m := pwResultLineRE.FindStringSubmatch(line); m != nil {
			if t, ok := parsePlaywrightTest(m[2]); ok {
				key := t.Name()
				switch m[1] {
				case "✘":
					if _, seen := lineFailed[key]; !seen {
						lineOrder = append(lineOrder, key)
					}
					lineFailed[key] = t
				case "✓":
					linePassed[key] = true
				}
			}
		}

		// `::error` annotations carry the assertion and code frame; keep the first per test
		if i := strings.Index(line, "::error "); i >= 0 {
			if m := pwErrorAnnotationRE.FindStringSubmatch(line[i:]); m != nil {
				if t, ok := parsePlaywrightTest(decodeAnnotation(m[1])); ok {
					key := t.Name()
					if _, seen := res.Errors[key]; !seen {
						res.Errors[key] = truncateRunes(strings.TrimSpace(decodeAnnotation(m[2])), maxPlaywrightErrorLen)
					}
				}
			}
		}
	}

	// Prefer the authoritative summary; otherwise rebuild from result lines
	if res.Summarized {
		res.Failed, res.Flaky = summaryFailed, summaryFlaky
		return res
	}
	for _, key := range lineOrder {
		if linePassed[key] {
			res.Flaky = append(res.Flaky, lineFailed[key])
		} else {
			res.Failed = append(res.Failed, lineFailed[key])
		}
	}
	return res
}

// parsePlaywrightTest splits "[project] › file:line:col › title".
func parsePlaywrightTest(s string) (PlaywrightTest, bool) {
	m := pwTestRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return PlaywrightTest{}, false
	}
	return PlaywrightTest{Project: m[1], File: m[2], Line: m[3] + ":" + m[4], Title: m[5]}, true
}

// decodeAnnotation reverses the escaping GitHub-style workflow commands use
// for property and message values.
func decodeAnnotation(s string) string {
	return strings.NewReplacer("%0A", "\n", "%0D", "\r", "%3A", ":", "%2C", ",", "%25", "%").Replace(s)
}

// truncateRunes shortens s to at most n runes, marking the cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n…(truncated)"
}

// PlaywrightSpecFailures groups a result's tests by spec file.
type PlaywrightSpecFailures struct {
	File   string
	Failed []PlaywrightTest
	Flaky  []PlaywrightTest
}

// BySpecFile groups failed and flaky tests by spec file, sorted by file.
func (r PlaywrightResult) BySpecFile() []PlaywrightSpecFailures {
	byFile := map[string]*PlaywrightSpecFailures{}
	get := func(file string) *PlaywrightSpecFailures {
		if g, ok := byFile[file]; ok {
			return g
		}
		g := &PlaywrightSpecFailures{File: file}
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
	out := make([]PlaywrightSpecFailures, 0, len(byFile))
	for _, g := range byFile {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}
