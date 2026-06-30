package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kurt/slakkr-ai/libs/ai"
	"github.com/kurt/slakkr-ai/libs/collectors"
	"github.com/kurt/slakkr-ai/libs/config/executionmode"
	"github.com/kurt/slakkr-ai/libs/config/userdata"
)

// directiveStatusTracker captures the latest known progress event per
// directive (and a start timestamp) so the run-log summary can report
// status, detail, and elapsed time for every enabled directive. It is
// safe for concurrent use because Collect runs directives in parallel.
type directiveStatusTracker struct {
	mu      sync.Mutex
	order   []string
	starts  map[string]time.Time
	latest  map[string]collectors.DirectiveProgress
	updated map[string]time.Time
}

func newDirectiveStatusTracker() *directiveStatusTracker {
	return &directiveStatusTracker{
		starts:  map[string]time.Time{},
		latest:  map[string]collectors.DirectiveProgress{},
		updated: map[string]time.Time{},
	}
}

func (t *directiveStatusTracker) Update(p collectors.DirectiveProgress, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.latest[p.DirectiveID]; !exists {
		t.order = append(t.order, p.DirectiveID)
		t.starts[p.DirectiveID] = now
	}
	t.latest[p.DirectiveID] = p
	t.updated[p.DirectiveID] = now
}

// Snapshot returns directive IDs in the order they were first seen
// alongside their last-known progress and elapsed duration.
func (t *directiveStatusTracker) Snapshot() []directiveSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]directiveSnapshot, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, directiveSnapshot{
			ID:       id,
			Progress: t.latest[id],
			Duration: t.updated[id].Sub(t.starts[id]),
		})
	}
	return out
}

type directiveSnapshot struct {
	ID       string
	Progress collectors.DirectiveProgress
	Duration time.Duration
}

// writeRunLogHeader records the resolved run options at the top of
// run.log so users can correlate logged HTTP/exec activity back to
// the run that produced them.
func writeRunLogHeader(w io.Writer, now time.Time, resolved executionmode.ResolvedRun, cfg userdata.ConfigFile, userdataDir, outPath string, noSave bool) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "# slakkr run %s\n\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintln(w, "## Mode")
	fmt.Fprintf(w, "  id:           %s\n", resolved.ModeID)
	fmt.Fprintf(w, "  name:         %s\n", resolved.ModeName)
	fmt.Fprintf(w, "  scope:        %s\n", string(resolved.Scope))
	fmt.Fprintf(w, "  formatter:    %s\n", fallback(resolved.Formatter, "(default)"))
	fmt.Fprintf(w, "  since:        %s\n", resolved.Since.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "  until:        %s\n", resolved.Until.UTC().Format(time.RFC3339))
	if resolved.LookbackDays > 0 {
		fmt.Fprintf(w, "  lookback:     %d day(s)\n", resolved.LookbackDays)
	}
	if instr := strings.TrimSpace(resolved.Instruction); instr != "" {
		fmt.Fprintf(w, "  instruction:  %s\n", truncateForLog(instr, 240))
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## AI provider")
	fmt.Fprintf(w, "  provider:           %s\n", fallback(cfg.AI.Provider, "rule-based"))
	if v := strings.TrimSpace(cfg.AI.ActivityFormatter); v != "" {
		fmt.Fprintf(w, "  activity_formatter: %s\n", v)
	}
	if v := strings.TrimSpace(cfg.AI.Ollama.BaseURL); v != "" {
		fmt.Fprintf(w, "  ollama.base_url:    %s\n", v)
	}
	if v := strings.TrimSpace(cfg.AI.Ollama.Model); v != "" {
		fmt.Fprintf(w, "  ollama.model:       %s\n", v)
	}
	if v := strings.TrimSpace(cfg.AI.Cursor.Command); v != "" {
		fmt.Fprintf(w, "  cursor.command:     %s\n", v)
	}
	if len(cfg.AI.Cursor.Args) > 0 {
		fmt.Fprintf(w, "  cursor.args:        %s\n", strings.Join(cfg.AI.Cursor.Args, " "))
	}
	if v := strings.TrimSpace(cfg.AI.Claude.Command); v != "" {
		fmt.Fprintf(w, "  claude.command:     %s\n", v)
	}
	if len(cfg.AI.Claude.Args) > 0 {
		fmt.Fprintf(w, "  claude.args:        %s\n", strings.Join(cfg.AI.Claude.Args, " "))
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Directives")
	enabled := 0
	for _, d := range cfg.Directives {
		state := "disabled"
		if d.Enabled {
			state = "enabled"
			enabled++
		}
		fmt.Fprintf(w, "- %s [%s] collector=%s state=%s\n", d.ID, fallback(d.Name, "(unnamed)"), d.Collector, state)
		if !d.Enabled {
			continue
		}
		if len(d.Target) > 0 {
			fmt.Fprintf(w, "    target: %s\n", redactedMap(d.Target))
		}
		if len(d.Config) > 0 {
			fmt.Fprintf(w, "    config: %s\n", redactedMap(d.Config))
		}
		if len(d.CredentialRefs) > 0 {
			keys := mapKeys(d.CredentialRefs)
			fmt.Fprintf(w, "    credentials: %s\n", strings.Join(keys, ", "))
		}
		if len(d.Paths) > 0 {
			fmt.Fprintf(w, "    paths: %s\n", strings.Join(d.Paths, ", "))
		}
		if v := strings.TrimSpace(d.CodeHome); v != "" {
			fmt.Fprintf(w, "    code_home: %s\n", v)
		}
	}
	fmt.Fprintf(w, "\nenabled directives: %d\n", enabled)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Output")
	fmt.Fprintf(w, "  userdata:   %s\n", userdataDir)
	if noSave {
		fmt.Fprintln(w, "  markdown:   (no-save)")
	} else {
		fmt.Fprintf(w, "  markdown:   %s\n", outPath)
	}
	fmt.Fprintln(w)
}

// writeRunLogCollectSummary appends a per-directive section after
// collection finishes, with item counts, terminal status, and elapsed
// time.
func writeRunLogCollectSummary(w io.Writer, tracker *directiveStatusTracker, statuses []collectors.StatusItem, collectDuration time.Duration) {
	if w == nil {
		return
	}
	counts := map[string]int{}
	errorCounts := map[string]int{}
	for _, s := range statuses {
		counts[s.DirectiveID]++
		if s.Kind == "collector_error" {
			errorCounts[s.DirectiveID]++
		}
	}
	fmt.Fprintln(w, "## Collection summary")
	fmt.Fprintf(w, "  total duration: %s\n", collectDuration.Round(time.Millisecond))
	fmt.Fprintf(w, "  total items:    %d\n\n", len(statuses))
	for _, snap := range tracker.Snapshot() {
		status := snap.Progress.Status
		if status == "" {
			status = "?"
		}
		fmt.Fprintf(w, "- %s status=%s items=%d errors=%d duration=%s\n",
			snap.ID,
			status,
			counts[snap.ID],
			errorCounts[snap.ID],
			snap.Duration.Round(time.Millisecond),
		)
		if detail := strings.TrimSpace(snap.Progress.Detail); detail != "" {
			fmt.Fprintf(w, "    detail: %s\n", truncateForLog(detail, 240))
		}
	}
	fmt.Fprintln(w)
}

func writeRunLogCollectError(w io.Writer, err error, collectDuration time.Duration) {
	if w == nil {
		return
	}
	fmt.Fprintln(w, "## Collection summary")
	fmt.Fprintf(w, "  total duration: %s\n", collectDuration.Round(time.Millisecond))
	fmt.Fprintf(w, "  result:         FAILED\n")
	fmt.Fprintf(w, "  error:          %s\n\n", truncateForLog(err.Error(), 480))
}

// writeRunLogFinalSummary records what the AI provider did and where
// (if anywhere) the markdown output was saved. Called after the AI
// call returns successfully and the output file has been written.
func writeRunLogFinalSummary(w io.Writer, provider ai.Provider, aiDuration time.Duration, outPath string, noSave bool) {
	if w == nil {
		return
	}
	fmt.Fprintln(w, "## AI summary")
	fmt.Fprintf(w, "  provider type: %T\n", provider)
	fmt.Fprintf(w, "  duration:      %s\n", aiDuration.Round(time.Millisecond))
	if noSave || outPath == "" {
		fmt.Fprintln(w, "  output:        (no-save)")
	} else {
		fmt.Fprintf(w, "  output:        %s\n", outPath)
	}
}

func writeRunLogAIError(w io.Writer, provider ai.Provider, err error, aiDuration time.Duration) {
	if w == nil {
		return
	}
	fmt.Fprintln(w, "## AI summary")
	fmt.Fprintf(w, "  provider type: %T\n", provider)
	fmt.Fprintf(w, "  duration:      %s\n", aiDuration.Round(time.Millisecond))
	fmt.Fprintf(w, "  result:        FAILED\n")
	fmt.Fprintf(w, "  error:         %s\n", truncateForLog(err.Error(), 480))
}

func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func truncateForLog(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func mapKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// redactedMap formats a map[string]string as `k=v` pairs, redacting
// values for keys that look like secrets. We default to showing values
// for non-secret keys because directive config (followed_repos,
// base_url, owner, etc.) is genuinely useful in the run log; secret
// values that ever land in directive.Config / .Target instead of
// .CredentialRefs are an outlier we still defend against.
func redactedMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := mapKeys(m)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		upper := strings.ToUpper(k)
		if strings.Contains(upper, "TOKEN") ||
			strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "PASSWORD") ||
			strings.HasSuffix(upper, "_KEY") {
			v = "REDACTED"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
