package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/kurt/slakkr-ai/libs/ai"
	"github.com/kurt/slakkr-ai/libs/collectors"
	"github.com/kurt/slakkr-ai/libs/config/configschema"
	"github.com/kurt/slakkr-ai/libs/config/docentconfig"
	"github.com/kurt/slakkr-ai/libs/config/executionmode"
	"github.com/kurt/slakkr-ai/apps/docent-reporter/internal/runlog"
	"github.com/kurt/slakkr-ai/libs/config/userdata"
	"github.com/kurt/slakkr-ai/apps/docent-reporter/internal/workflow"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

type App struct {
	Out io.Writer
	Err io.Writer
	In  io.Reader
	Now func() time.Time
	AI  ai.Provider
	Reg *collectors.Registry
}

func New(out, errOut io.Writer, in io.Reader) *App {
	now := time.Now
	return &App{
		Out: out,
		Err: errOut,
		In:  in,
		Now: now,
		AI:  ai.RuleBasedProvider{},
		Reg: collectors.NewRegistry(now),
	}
}

func (a *App) Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("docent-reporter", flag.ContinueOnError)
	fs.SetOutput(a.Err)

	userdataDir := fs.String("userdata", "", "legacy combined dir holding config.yaml/.env/logs/output (overrides --config-dir/--state-dir/--out-dir when set)")
	configDirFlag := fs.String("config-dir", "", "directory holding config.yaml + .env (default ~/.config/docent)")
	stateDirFlag := fs.String("state-dir", "", "directory for run logs (default ~/.local/state/docent)")
	outDirFlag := fs.String("out-dir", "", "directory for generated markdown (default config output_dir, then ~/docent)")
	var configPath string
	fs.StringVar(&configPath, "config", "", "config file path (default <config-dir>/config.yaml)")
	fs.StringVar(&configPath, "c", "", "shorthand for --config")
	modeFlag := fs.String("mode", "", "execution mode id (built-in: daily-plan, recent-activity, custom-prompt, prs; plus any user-declared)")
	outPath := fs.String("out", "", "output markdown path (default <out-dir>/<date>-<mode>.md; if the file exists, -2, -3, … are appended before the extension)")
	noSave := fs.Bool("no-save", false, "do not write output file")
	dateStr := fs.String("date", "", "date label YYYY-MM-DD for output filename (default today)")

	days := fs.Int("days", 0, "lookback days override (0 = use mode default or prompt)")
	promptFlag := fs.String("prompt", "", "instruction override for the LLM (replaces the mode's prompt for this run)")
	promptFile := fs.String("prompt-file", "", "read instruction override from file")
	checkOnly := fs.Bool("check", false, "validate every enabled directive and exit without collecting data")
	skipCheck := fs.Bool("skip-check", false, "skip the pre-flight directive validation that runs alongside the interactive menu")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Resolve config / state / output locations. Passing --userdata selects
	// the legacy all-in-one layout (config.yaml, .env, logs/, output/ all
	// under that dir), preserving older invocations and tests. Otherwise we
	// use XDG-style defaults:
	//   config + .env -> ~/.config/docent      (docentconfig.DefaultDir)
	//   run logs      -> ~/.local/state/docent  (docentconfig.StateDir)
	//   markdown out  -> ~/docent               (or config output_dir / --out-dir)
	legacy := strings.TrimSpace(*userdataDir) != ""
	var configDir, logsParent string
	if legacy {
		configDir = *userdataDir
		logsParent = *userdataDir
	} else {
		configDir = strings.TrimSpace(*configDirFlag)
		if configDir == "" {
			configDir = docentconfig.DefaultDir()
		}
		logsParent = docentconfig.StateDir()
	}
	if v := strings.TrimSpace(*stateDirFlag); v != "" {
		logsParent = v
	}
	// Collectors resolve credential_refs from <envDir>/.env (and cache there).
	envDir := configDir

	store := userdata.NewStore(configDir)
	// Opportunistically remove the legacy `.cache/` directory next to the
	// config. Errors (permission denied, already removed) are ignored.
	_ = os.RemoveAll(filepath.Join(configDir, ".cache"))

	configPath = strings.TrimSpace(configPath)
	var (
		cfg       userdata.ConfigFile
		cfgSource string
		err       error
	)
	if configPath != "" {
		cfgSource = configPath
		cfg, err = loadConfigFromPath(configPath)
	} else {
		if err := store.EnsureConfig(ctx); err != nil {
			return err
		}
		cfgSource = store.ConfigPath()
		cfg, err = store.LoadConfig()
	}
	if err != nil {
		return err
	}
	if err := mergeDirectivesFromLegacyFile(configDir, &cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%s: %w", cfgSource, err)
	}
	if len(cfg.Directives) == 0 {
		return fmt.Errorf("no directives in %s: add a `directives:` list", cfgSource)
	}

	modes, err := executionmode.Load(executionmode.BuiltinModes(), cfg.ExecutionModes)
	if err != nil {
		return fmt.Errorf("%s: %w", cfgSource, err)
	}

	expand := expandRepoPathFromStdin(a.In)
	validateOpts := &collectors.ValidateOpts{
		UserdataDir:    envDir,
		ExpandRepoPath: expand,
	}

	if *checkOnly {
		issues := runFullValidation(ctx, a.Reg, cfg, validateOpts)
		a.renderValidationIssues(issues)
		if len(issues) > 0 {
			return fmt.Errorf("%d validation issue(s)", len(issues))
		}
		fmt.Fprintln(a.Out, "All enabled directives passed validation.")
		return nil
	}

	var validation *validationRunner
	if !*skipCheck {
		validation = startValidation(ctx, a.Reg, cfg, validateOpts)
	}

	modeID := strings.TrimSpace(*modeFlag)
	if modeID == "" {
		if !a.stdinIsTerminal() {
			return fmt.Errorf("--mode is required when stdin is not a TTY")
		}
		labels := make([]string, len(modes))
		for i, m := range modes {
			labels[i] = formatModeOption(m)
		}
		var pick string
		if err := survey.AskOne(&survey.Select{
			Message: "Choose mode",
			Options: labels,
		}, &pick, survey.WithStdio(os.Stdin, os.Stdout, os.Stderr)); err != nil {
			return err
		}
		modeID = modeIDFromOption(pick)
	}

	mode, ok := executionmode.Find(modes, modeID)
	if !ok {
		available := make([]string, len(modes))
		for i, m := range modes {
			available[i] = m.ID
		}
		return fmt.Errorf("unknown mode %q (available: %s)", modeID, strings.Join(available, ", "))
	}

	outDate := strings.TrimSpace(*dateStr)
	if outDate == "" {
		outDate = a.Now().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", outDate); err != nil {
		return fmt.Errorf("--date must be YYYY-MM-DD: %w", err)
	}

	provider := ai.SelectProvider(cfg.AI, a.AI)
	var stream io.Writer
	switch provider.(type) {
	case ai.OllamaProvider, ai.CursorCLIProvider, ai.ClaudeCLIProvider:
		stream = a.Err
	}

	promptOverride, err := readPromptOverride(*promptFlag, *promptFile)
	if err != nil {
		return err
	}

	var resolvePrompter executionmode.Prompter
	if a.stdinIsTerminal() {
		resolvePrompter = StdioPrompter{In: a.In, Out: a.Out}
	}
	resolved, err := executionmode.Resolve(mode, executionmode.ResolveOpts{
		Now:                     a.Now(),
		Prompter:                resolvePrompter,
		DaysOverride:            *days,
		PromptOverride:          promptOverride,
		ConfigActivityFormatter: cfg.AI.ActivityFormatter,
	})
	if err != nil {
		return err
	}

	if validation != nil {
		issues := validation.Wait(ctx)
		if len(issues) > 0 {
			a.renderValidationIssues(issues)
			if !a.stdinIsTerminal() {
				return fmt.Errorf("%d directive validation issue(s); rerun with --skip-check to ignore or --check for a focused report", len(issues))
			}
			prompter := StdioPrompter{In: a.In, Out: a.Out}
			proceed, err := prompter.Confirm("Proceed despite validation warnings?", false)
			if err != nil {
				return err
			}
			if !proceed {
				return fmt.Errorf("aborted: validation reported %d issue(s)", len(issues))
			}
		}
	}

	// Resolve the output path (and its uniquified collision suffix)
	// before collection so the run-log directory name matches the
	// markdown file we will eventually save. When --no-save is set we
	// still derive a basename so logs still get a stable home.
	outputDir := resolveOutputDir(legacy, *userdataDir, *outDirFlag, cfg.OutputDir)
	outPathResolved, outBasename, err := resolveOutputPath(outputDir, *outPath, outDate, resolved.ModeID, *noSave)
	if err != nil {
		return err
	}

	run, err := runlog.NewRun(logsParent, outBasename, a.Now())
	if err != nil {
		return err
	}
	defer run.Close()

	tracker := newDirectiveStatusTracker()
	progress := newDirectiveProgressTable(a.Out)
	defer progress.Done()

	writeRunLogHeader(run.RunInfo(), a.Now(), resolved, cfg, configDir, outPathResolved, *noSave)

	// Collection runs under its own cancellable context so the user can
	// abort slow collectors (e.g. Slack) with a keypress and still proceed
	// to run the prompt against whatever was gathered. The abort must not
	// cancel the AI run below, so we keep the parent ctx for that.
	collectCtx, collectCancel := context.WithCancel(ctx)
	defer collectCancel()
	if a.stdinIsTerminal() {
		fmt.Fprintln(a.Out, abortKeyHint)
	}
	stopAbort := startAbortListener(a.In, collectCancel)

	collectStart := a.Now()
	collectMode := collectors.ModeEvents
	if resolved.ModeID == executionmode.BuiltinPRs {
		// The `prs` mode lists current open PRs (state view) rather than
		// the activity timeline.
		collectMode = collectors.ModeState
	}
	statuses, err := workflow.CollectStatuses(collectCtx, workflow.Deps{
		Registry: a.Reg,
		Now:      a.Now,
		ExpandRepoPath: expand,
		OnDirectiveUpdate: func(p collectors.DirectiveProgress) {
			tracker.Update(p, a.Now())
			progress.Update(p)
		},
		RunLog: runLogAdapter{run: run},
	}, cfg, envDir, workflow.RunOptions{
		Since:              resolved.Since,
		Until:              resolved.Until,
		Scope:              collectors.Scope(resolved.Scope),
		OnlyCollectorTypes: resolved.Collectors,
		Mode:               collectMode,
	})
	stopAbort()
	collectDuration := a.Now().Sub(collectStart)
	if err != nil {
		writeRunLogCollectError(run.RunInfo(), err, collectDuration)
		return err
	}
	progress.Done()
	if collectCtx.Err() != nil {
		fmt.Fprintln(a.Err, "Collection aborted; running the prompt against the data collected so far.")
	}

	writeRunLogCollectSummary(run.RunInfo(), tracker, statuses, collectDuration)

	// Scope semantics are now honored by the collectors themselves; the
	// CLI no longer needs to post-filter the aggregated status list.
	// collectors.FilterToSelf remains exported as a fallback helper for
	// callers (tests, ad-hoc tooling) that want the old behavior.

	// Per-run provider formatter override: SelectProvider picks formatter
	// from ai.activity_formatter; ExecutionMode may override it for this
	// run only.
	if resolved.Formatter != "" {
		provider = withFormatter(provider, ai.SelectActivityFormatter(resolved.Formatter))
	}

	aiStart := a.Now()
	md, err := provider.RunMode(ctx, ai.RunInput{
		ModeID:       resolved.ModeID,
		ModeName:     resolved.ModeName,
		Now:          resolved.Until,
		Since:        resolved.Since,
		LookbackDays: resolved.LookbackDays,
		Instruction:  resolved.Instruction,
		Statuses:     statuses,
		DebugDir:     run.Dir(),
		StreamOut:    stream,
	})
	aiDuration := a.Now().Sub(aiStart)
	if err != nil {
		writeRunLogAIError(run.RunInfo(), provider, err, aiDuration)
		return err
	}
	md = strings.TrimSpace(md) + "\n"

	fmt.Fprint(a.Out, md)

	finalOutPath := ""
	if !*noSave {
		if err := os.MkdirAll(filepath.Dir(outPathResolved), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(outPathResolved, []byte(md), 0o644); err != nil {
			return err
		}
		finalOutPath = outPathResolved
		fmt.Fprintf(a.Err, "Wrote %s\n", outPathResolved)
	}

	writeRunLogFinalSummary(run.RunInfo(), provider, aiDuration, finalOutPath, *noSave)

	if err := runlog.PruneRunLogs(logsParent, runlog.DefaultRetention); err != nil {
		fmt.Fprintf(a.Err, "warning: prune run logs: %v\n", err)
	}

	return nil
}

// runLogAdapter bridges *runlog.Run to collectors.RunLog so the
// collectors package doesn't import runlog directly.
type runLogAdapter struct {
	run *runlog.Run
}

func (a runLogAdapter) Directive(id string) collectors.DirectiveLogger {
	return a.run.Directive(id)
}

// resolveOutputDir decides the directory generated markdown lands in.
// Precedence: --out-dir flag, then (legacy) <userdata>/output, then the
// config's output_dir, then ~/docent. Tilde paths are expanded.
func resolveOutputDir(legacy bool, userdataDir, outDirFlag, configOutputDir string) string {
	if v := strings.TrimSpace(outDirFlag); v != "" {
		return expandPath(v)
	}
	if legacy {
		return filepath.Join(userdataDir, "output")
	}
	if v := strings.TrimSpace(configOutputDir); v != "" {
		return expandPath(v)
	}
	return docentconfig.DefaultOutputDir()
}

// expandPath expands a leading ~ (or ~/) to the user's home directory.
func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// resolveOutputPath decides where the markdown output will land and
// what basename anchors the run-log directory. When --no-save is set
// we still derive a basename from outDate + modeID so logs have a
// home. When --out is explicit, its directory is honored. In both
// save cases, an existing file triggers `-2`, `-3`, … suffixing
// (uniqueOutputPath) so the run-log dir name matches the file we
// actually create.
func resolveOutputPath(outputDir, outFlag, outDate, modeID string, noSave bool) (path, basename string, err error) {
	out := strings.TrimSpace(outFlag)
	defaultName := fmt.Sprintf("%s-%s.md", outDate, modeID)
	if out == "" {
		out = filepath.Join(outputDir, defaultName)
	}
	if !noSave {
		out, err = uniqueOutputPath(out)
		if err != nil {
			return "", "", err
		}
	}
	base := filepath.Base(out)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" {
		stem = strings.TrimSuffix(defaultName, ".md")
	}
	return out, stem, nil
}

// formatModeOption renders an interactive-menu label that includes both the
// mode's display name and its ID. modeIDFromOption extracts the ID back
// from such a label.
func formatModeOption(m executionmode.ExecutionMode) string {
	name := strings.TrimSpace(m.Name)
	if name == "" || name == m.ID {
		return m.ID
	}
	return fmt.Sprintf("%s (%s)", name, m.ID)
}

func modeIDFromOption(option string) string {
	option = strings.TrimSpace(option)
	if l := strings.LastIndex(option, "("); l >= 0 && strings.HasSuffix(option, ")") {
		return strings.TrimSpace(option[l+1 : len(option)-1])
	}
	return option
}

// readPromptOverride combines --prompt and --prompt-file into a single
// instruction-override string; --prompt-file (when set) takes precedence
// over --prompt to match the previous custom-prompt semantics.
func readPromptOverride(promptFlag, promptFile string) (string, error) {
	if pf := strings.TrimSpace(promptFile); pf != "" {
		b, err := os.ReadFile(pf)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return promptFlag, nil
}

// withFormatter returns a provider whose ActivityFormatter has been
// overridden. Only the three concrete provider types in this package are
// handled; other implementations are returned unchanged.
func withFormatter(p ai.Provider, f ai.ActivityFormatter) ai.Provider {
	switch pp := p.(type) {
	case ai.RuleBasedProvider:
		pp.Formatter = f
		return pp
	case ai.OllamaProvider:
		pp.Formatter = f
		return pp
	case ai.CursorCLIProvider:
		pp.Formatter = f
		return pp
	case ai.ClaudeCLIProvider:
		pp.Formatter = f
		return pp
	default:
		return p
	}
}

// uniqueOutputPath returns path if nothing exists at that path yet; otherwise it
// returns the same basename with -2, -3, … inserted before the extension.
func uniqueOutputPath(path string) (string, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	candidate := path
	for n := 2; ; n++ {
		_, err := os.Stat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return candidate, nil
			}
			return "", err
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, n, ext))
	}
}

func mergeDirectivesFromLegacyFile(userdataDir string, cfg *userdata.ConfigFile) error {
	if len(cfg.Directives) > 0 {
		return nil
	}
	path := filepath.Join(userdataDir, "directives.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var legacy struct {
		Directives []userdata.Directive `yaml:"directives"`
	}
	if err := yaml.Unmarshal(raw, &legacy); err != nil {
		return fmt.Errorf("parse directives.yaml: %w", err)
	}
	cfg.Directives = legacy.Directives
	return nil
}

func loadConfigFromPath(path string) (userdata.ConfigFile, error) {
	var cfg userdata.ConfigFile
	content, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return cfg, nil
	}
	if err := configschema.ValidateYAML(content); err != nil {
		return cfg, userdata.ValidationError{Problems: configschema.ValidationProblems(err)}
	}
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func expandRepoPathFromStdin(in io.Reader) func(string) string {
	return func(path string) string {
		path = strings.TrimSpace(path)
		if path == "" {
			return ""
		}
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return path
			}
			path = filepath.Join(home, path[2:])
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		return abs
	}
}

type validationRunner struct {
	done   chan struct{}
	issues []collectors.ValidationIssue
	cancel context.CancelFunc
}

// startValidation runs Registry.Validate and ai.Validate on a background
// goroutine so the interactive menu can collect user input while validators
// probe dependencies, credentials, and the configured AI provider in parallel.
// The returned runner is later joined with Wait, which blocks until validation
// finishes (or ctx is cancelled).
func startValidation(parent context.Context, reg *collectors.Registry, cfg userdata.ConfigFile, opts *collectors.ValidateOpts) *validationRunner {
	ctx, cancel := context.WithCancel(parent)
	r := &validationRunner{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(r.done)
		r.issues = runFullValidation(ctx, reg, cfg, opts)
	}()
	return r
}

// runFullValidation joins the AI provider check with the directive checks,
// returning AI issues first (they apply globally) followed by per-directive
// issues in directive order.
func runFullValidation(ctx context.Context, reg *collectors.Registry, cfg userdata.ConfigFile, opts *collectors.ValidateOpts) []collectors.ValidationIssue {
	aiCh := make(chan []collectors.ValidationIssue, 1)
	go func() {
		raw := ai.Validate(ctx, cfg.AI, nil)
		aiCh <- aiIssuesAsValidation(raw)
	}()
	directiveIssues := reg.Validate(ctx, cfg.Directives, opts)
	aiIssues := <-aiCh
	return append(aiIssues, directiveIssues...)
}

func aiIssuesAsValidation(issues []ai.Issue) []collectors.ValidationIssue {
	if len(issues) == 0 {
		return nil
	}
	out := make([]collectors.ValidationIssue, 0, len(issues))
	for _, iss := range issues {
		out = append(out, collectors.ValidationIssue{
			DirectiveID: "ai",
			Description: "AI provider",
			Collector:   "ai/" + iss.Provider,
			Field:       iss.Field,
			Message:     iss.Message,
			Remediation: iss.Remediation,
		})
	}
	return out
}

func (r *validationRunner) Wait(ctx context.Context) []collectors.ValidationIssue {
	if r == nil {
		return nil
	}
	select {
	case <-r.done:
		return r.issues
	case <-ctx.Done():
		r.cancel()
		<-r.done
		return r.issues
	}
}

func (a *App) renderValidationIssues(issues []collectors.ValidationIssue) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintln(a.Out, "Validation warnings:")
	for _, iss := range issues {
		label := strings.TrimSpace(iss.DirectiveID)
		if d := strings.TrimSpace(iss.Description); d != "" && d != label {
			label = fmt.Sprintf("%s (%s)", label, d)
		}
		if label == "" {
			label = iss.Collector
		}
		field := ""
		if strings.TrimSpace(iss.Field) != "" {
			field = fmt.Sprintf(" [%s]", iss.Field)
		}
		fmt.Fprintf(a.Out, "  ! %s [%s]%s: %s\n", label, iss.Collector, field, iss.Message)
		if rem := strings.TrimSpace(iss.Remediation); rem != "" {
			fmt.Fprintf(a.Out, "      -> %s\n", rem)
		}
	}
	fmt.Fprintln(a.Out)
}

func (a *App) stdinIsTerminal() bool {
	f, ok := a.In.(*os.File)
	if !ok || f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

type directiveProgressTable struct {
	out           io.Writer
	mu            sync.Mutex
	order         []string
	rows          map[string]collectors.DirectiveProgress
	interactive   bool
	colorEnabled  bool
	renderedLines int
	finished      bool
}

func newDirectiveProgressTable(out io.Writer) *directiveProgressTable {
	interactive := false
	if f, ok := out.(*os.File); ok {
		interactive = term.IsTerminal(int(f.Fd()))
	}
	return &directiveProgressTable{
		out:          out,
		rows:         map[string]collectors.DirectiveProgress{},
		interactive:  interactive,
		colorEnabled: interactive && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
	}
}

func (t *directiveProgressTable) Update(p collectors.DirectiveProgress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.rows[p.DirectiveID]; !exists {
		t.order = append(t.order, p.DirectiveID)
	}
	t.rows[p.DirectiveID] = p
	t.renderLocked()
}

func (t *directiveProgressTable) Done() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	if len(t.order) == 0 {
		return
	}
	t.finished = true
	fmt.Fprintln(t.out)
}

func (t *directiveProgressTable) renderLocked() {
	lines := 2 + len(t.order)
	if t.interactive && t.renderedLines > 0 {
		fmt.Fprintf(t.out, "\x1b[%dA", t.renderedLines)
	}
	// Only render a Progress column when at least one collector has
	// reported a denominator. Keeps the table compact in the
	// degenerate case where nothing emits progress (e.g. tests with
	// fake collectors) and lines up bar widths so columns stay aligned
	// across rows.
	showBars := false
	for _, id := range t.order {
		if t.rows[id].Total > 0 {
			showBars = true
			break
		}
	}
	t.printTableLine("Collectors")
	if showBars {
		t.printTableLine("Indicator | Task Description | Progress | Status")
	} else {
		t.printTableLine("Indicator | Task Description | Status")
	}
	for _, id := range t.order {
		row := t.rows[id]
		if showBars {
			t.printTableLine(fmt.Sprintf(
				"%s | %s | %s | %s",
				t.statusIcon(row.Status),
				row.Description,
				t.progressBar(row),
				t.statusLabel(row),
			))
		} else {
			t.printTableLine(fmt.Sprintf(
				"%s | %s | %s",
				t.statusIcon(row.Status),
				row.Description,
				t.statusLabel(row),
			))
		}
	}
	if !t.interactive {
		fmt.Fprintln(t.out)
	}
	t.renderedLines = lines
}

func (t *directiveProgressTable) printTableLine(line string) {
	if !t.interactive {
		fmt.Fprintln(t.out, line)
		return
	}
	fmt.Fprintf(t.out, "\x1b[2K\r%s\n", line)
}

// progressBarWidth is the printable cell width of a rendered bar
// (filled+empty glyphs). The same width is used for every row so the
// "| Status" column stays vertically aligned regardless of the
// underlying completed/total numbers.
const progressBarWidth = 16

// progressBar renders a fixed-width unicode bar plus a "(done/total)"
// suffix when the collector reported a denominator. Rows without a
// denominator yield an empty string so the caller can still print the
// cell at the same width (padding handled by progressCell).
func (t *directiveProgressTable) progressBar(p collectors.DirectiveProgress) string {
	if p.Total <= 0 {
		// Reserve the column width with whitespace so columns line up.
		return strings.Repeat(" ", progressBarWidth+len(" (?/?)"))
	}
	completed := p.Completed
	if completed < 0 {
		completed = 0
	}
	if completed > p.Total {
		completed = p.Total
	}
	filled := (completed * progressBarWidth) / p.Total
	if filled > progressBarWidth {
		filled = progressBarWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", progressBarWidth-filled)
	suffix := fmt.Sprintf(" (%d/%d)", completed, p.Total)
	if t.colorEnabled {
		switch p.Status {
		case "done":
			bar = colorize(bar, ansiGreen)
		case "error":
			bar = colorize(bar, ansiRed)
		default:
			bar = colorize(bar, ansiYellow)
		}
	}
	return bar + suffix
}

func (t *directiveProgressTable) statusIcon(status string) string {
	icon := "⟳"
	switch status {
	case "done":
		icon = "✓"
	case "error":
		icon = "✗"
	}
	if !t.colorEnabled {
		return icon
	}
	switch status {
	case "done":
		return colorize(icon, ansiGreen)
	case "error":
		return colorize(icon, ansiRed)
	default:
		return colorize(icon, ansiYellow)
	}
}

func (t *directiveProgressTable) statusLabel(p collectors.DirectiveProgress) string {
	label := ""
	if strings.TrimSpace(p.Detail) == "" {
		switch p.Status {
		case "done":
			label = "done"
		case "error":
			label = "error"
		default:
			label = "running"
		}
	} else {
		label = p.Detail
	}
	if !t.colorEnabled {
		return label
	}
	switch p.Status {
	case "done":
		return colorize(label, ansiGreen)
	case "error":
		return colorize(label, ansiRed)
	default:
		return colorize(label, ansiYellow)
	}
}

const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

func colorize(value, color string) string {
	return color + value + ansiReset
}

func Main() {
	app := New(os.Stdout, os.Stderr, os.Stdin)
	if err := app.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
