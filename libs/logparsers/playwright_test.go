package logparsers

import (
	"strings"
	"testing"
)

func logLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestParsePlaywrightLogSummaryFailedAndFlaky(t *testing.T) {
	log := logLines(
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
	r := ParsePlaywright(log)

	// The summary block is authoritative
	if !r.Complete {
		t.Fatalf("expected summary to be parsed")
	}
	if len(r.Failed) != 1 || r.Failed[0].Name != "e2e/widgets.spec.ts:10:3 › adds a widget" || r.Failed[0].File != "e2e/widgets.spec.ts" {
		t.Fatalf("failed = %+v", r.Failed)
	}
	if len(r.Flaky) != 1 || r.Flaky[0].Name != "e2e/gadgets.spec.ts:40:1 › opens a gadget" {
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
	log := logLines(
		"  ✘    1 [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget (12.0s)",
		"  ✘    2 [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget (retry #1) (11.0s)",
		"  ✘    3 [chromium] › e2e/gadgets.spec.ts:40:1 › opens a gadget (5.2s)",
		"  ✓    4 [chromium] › e2e/gadgets.spec.ts:40:1 › opens a gadget (retry #1) (4.1s)",
		"  ✓    5 [chromium] › e2e/gadgets.spec.ts:60:1 › closes a gadget (1.0s)",
	)
	r := ParsePlaywright(log)
	if r.Complete {
		t.Fatalf("expected fallback parse")
	}
	if len(r.Failed) != 1 || r.Failed[0].File != "e2e/widgets.spec.ts" {
		t.Fatalf("failed = %+v", r.Failed)
	}
	if len(r.Flaky) != 1 || r.Flaky[0].File != "e2e/gadgets.spec.ts" {
		t.Fatalf("flaky = %+v", r.Flaky)
	}
}

func TestParsePlaywrightLogCleanRun(t *testing.T) {
	r := ParsePlaywright(logLines("  ✓    1 [chromium] › e2e/widgets.spec.ts:10:3 › adds a widget (1.0s)", "  1 passed (3.0s)"))
	if !r.Empty() {
		t.Fatalf("expected no failures, got %+v", r)
	}
}

func TestResultByFile(t *testing.T) {
	r := Result{
		Failed: []Test{{File: "e2e/b.spec.ts", Name: "x"}},
		Flaky:  []Test{{File: "e2e/a.spec.ts", Name: "y"}, {File: "e2e/b.spec.ts", Name: "z"}},
	}
	specs := r.ByFile()
	if len(specs) != 2 || specs[0].File != "e2e/a.spec.ts" || specs[1].File != "e2e/b.spec.ts" {
		t.Fatalf("specs = %+v", specs)
	}
	if len(specs[1].Failed) != 1 || len(specs[1].Flaky) != 1 {
		t.Fatalf("b.spec grouping = %+v", specs[1])
	}
}

func TestRegistry(t *testing.T) {
	if _, ok := Get(" Playwright "); !ok {
		t.Fatalf("playwright parser not registered")
	}
	if _, ok := Get("nope"); ok {
		t.Fatalf("unknown parser found")
	}
	if names := Names(); len(names) == 0 || names[0] != "playwright" {
		t.Fatalf("names = %v", names)
	}
}

func TestResultMerge(t *testing.T) {
	// A sharded run is complete only if every shard's log was
	r := Result{Complete: true}
	r.Merge(Result{Failed: []Test{{File: "a", Name: "a1"}}, Errors: map[string]string{"a1": "first"}, Complete: true})
	r.Merge(Result{Flaky: []Test{{File: "b", Name: "b1"}}, Errors: map[string]string{"a1": "second"}})
	if len(r.Failed) != 1 || len(r.Flaky) != 1 || r.Complete || r.Errors["a1"] != "first" {
		t.Fatalf("merged = %+v", r)
	}
}
