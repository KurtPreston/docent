package logparsers

import (
	"regexp"
	"strings"
)

var (
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

// ParsePlaywright extracts failed and flaky tests from the output of
// Playwright's list (or line) reporter as captured in a CI log.
func ParsePlaywright(log string) Result {
	res := Result{Errors: map[string]string{}}
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")

	// Per-test outcomes from the result lines, used when no summary exists
	lineFailed := map[string]Test{}
	linePassed := map[string]bool{}
	var lineOrder []string

	// Walk the log once, collecting summary sections, result lines, and errors
	section := ""
	var summaryFailed, summaryFlaky []Test
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")

		// The final summary lists each failed/flaky test under its count line
		if m := pwSummaryCountRE.FindStringSubmatch(line); m != nil {
			section = m[2]
			if section == "failed" || section == "flaky" {
				res.Complete = true
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
				key := t.Name
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
					key := t.Name
					if _, seen := res.Errors[key]; !seen {
						res.Errors[key] = Truncate(strings.TrimSpace(decodeAnnotation(m[2])), maxPlaywrightErrorLen)
					}
				}
			}
		}
	}

	// Prefer the authoritative summary; otherwise rebuild from result lines
	if res.Complete {
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

// parsePlaywrightTest reads "[project] › file:line:col › title" into a Test
// named "file:line:col › title". The project is dropped so a test that ran
// in several projects reads the same everywhere.
func parsePlaywrightTest(s string) (Test, bool) {
	m := pwTestRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Test{}, false
	}
	return Test{File: m[2], Name: m[2] + ":" + m[3] + ":" + m[4] + " › " + m[5]}, true
}

// decodeAnnotation reverses the escaping GitHub-style workflow commands use
// for property and message values.
func decodeAnnotation(s string) string {
	return strings.NewReplacer("%0A", "\n", "%0D", "\r", "%3A", ":", "%2C", ",", "%25", "%").Replace(s)
}
