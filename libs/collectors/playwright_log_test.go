package collectors

import (
	"strings"
	"testing"
)

// adoLog prefixes each line with an ADO-style timestamp, as pipeline logs do.
func adoLog(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("2026-01-02T03:04:05.0000000Z " + l + "\n")
	}
	return b.String()
}

func TestParsePlaywrightLogSummaryFailedAndFlaky(t *testing.T) {
	log := adoLog(
		"Running 12 tests using 4 workers",
		"  ✘    3 [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget (12.0s)::error file=e2e/widgets.spec.ts,title=[chromium] › e2e/widgets.spec.ts:10:3 › adds a widget,line=10,col=3::1) [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget %0A%0A    Error: expect(locator).toBeVisible() failed",
		"  ✘    7 [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget (retry #1) (1.0m)",
		"  ✘    8 [chromium] › e2e/gadgets.spec.ts:40:1 › opens a gadget (5.2s)",
		"  ✓    9 [chromium] › e2e/gadgets.spec.ts:40:1 › opens a gadget (retry #1) (4.1s)",
		"  1 failed",
		"    [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget ",
		"  1 flaky",
		"    [chromium] › e2e/gadgets.spec.ts:40:1 › opens a gadget ",
		"  2 skipped",
		"  8 passed (2.1m)",
	)
	r := ParsePlaywrightLog(log)

	// The summary block is authoritative
	if !r.Summarized {
		t.Fatalf("expected summary to be parsed")
	}
	if len(r.Failed) != 1 || r.Failed[0].Name() != "e2e/widgets.spec.ts:10:3 › adds a widget" || r.Failed[0].Project != "chromium" {
		t.Fatalf("failed = %+v", r.Failed)
	}
	if len(r.Flaky) != 1 || r.Flaky[0].File != "e2e/gadgets.spec.ts" || r.Flaky[0].Title != "opens a gadget" {
		t.Fatalf("flaky = %+v", r.Flaky)
	}

	// The error annotation is decoded and keyed by test name
	msg := r.Errors["e2e/widgets.spec.ts:10:3 › adds a widget"]
	if !strings.Contains(msg, "\n    Error: expect(locator).toBeVisible() failed") {
		t.Fatalf("error not decoded: %q", msg)
	}
}

func TestParsePlaywrightLogFallbackWithoutSummary(t *testing.T) {
	// A run cut off before its summary is rebuilt from result lines
	log := adoLog(
		"  ✘    1 [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget (12.0s)",
		"  ✘    2 [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget (retry #1) (11.0s)",
		"  ✘    3 [chromium] › e2e/gadgets.spec.ts:40:1 › opens a gadget (5.2s)",
		"  ✓    4 [chromium] › e2e/gadgets.spec.ts:40:1 › opens a gadget (retry #1) (4.1s)",
		"  ✓    5 [chromium] › e2e/gadgets.spec.ts:60:1 › closes a gadget (1.0s)",
	)
	r := ParsePlaywrightLog(log)
	if r.Summarized {
		t.Fatalf("expected fallback parse")
	}
	if len(r.Failed) != 1 || r.Failed[0].File != "e2e/widgets.spec.ts" {
		t.Fatalf("failed = %+v", r.Failed)
	}
	if len(r.Flaky) != 1 || r.Flaky[0].Title != "opens a gadget" {
		t.Fatalf("flaky = %+v", r.Flaky)
	}
}

func TestParsePlaywrightLogCleanRun(t *testing.T) {
	r := ParsePlaywrightLog(adoLog("  ✓    1 [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget (1.0s)", "  1 passed (3.0s)"))
	if !r.Empty() {
		t.Fatalf("expected no failures, got %+v", r)
	}
}

func TestPlaywrightResultBySpecFile(t *testing.T) {
	r := PlaywrightResult{
		Failed: []PlaywrightTest{{File: "e2e/b.spec.ts", Line: "1:1", Title: "x"}},
		Flaky:  []PlaywrightTest{{File: "e2e/a.spec.ts", Line: "2:1", Title: "y"}, {File: "e2e/b.spec.ts", Line: "3:1", Title: "z"}},
	}
	specs := r.BySpecFile()
	if len(specs) != 2 || specs[0].File != "e2e/a.spec.ts" || specs[1].File != "e2e/b.spec.ts" {
		t.Fatalf("specs = %+v", specs)
	}
	if len(specs[1].Failed) != 1 || len(specs[1].Flaky) != 1 {
		t.Fatalf("b.spec grouping = %+v", specs[1])
	}
}
