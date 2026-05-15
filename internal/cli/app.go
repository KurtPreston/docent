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
	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/configschema"
	"github.com/kurt/slakkr-ai/internal/executionmode"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"github.com/kurt/slakkr-ai/internal/workflow"
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
	fs := flag.NewFlagSet("slakkr", flag.ContinueOnError)
	fs.SetOutput(a.Err)

	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	var configPath string
	fs.StringVar(&configPath, "config", "", "config file path (default <userdata>/config.yaml)")
	fs.StringVar(&configPath, "c", "", "shorthand for --config")
	modeFlag := fs.String("mode", "", "execution mode id (built-in: daily-plan, recent-activity, custom-prompt; plus any user-declared)")
	outPath := fs.String("out", "", "output markdown path (default userdata/output/<date>-<mode>.md; if the file exists, -2, -3, … are appended before the extension)")
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

	store := userdata.NewStore(*userdataDir)
	if err := os.MkdirAll(filepath.Join(*userdataDir, "output"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*userdataDir, ".cache"), 0o755); err != nil {
		return err
	}

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
		if err := store.Ensure(ctx); err != nil {
			return err
		}
		cfgSource = store.ConfigPath()
		cfg, err = store.LoadConfig()
	}
	if err != nil {
		return err
	}
	if err := mergeDirectivesFromLegacyFile(*userdataDir, &cfg); err != nil {
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
		UserdataDir:    *userdataDir,
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
	case ai.OllamaProvider, ai.CursorCLIProvider:
		stream = a.Err
	}

	debugDir := filepath.Join(*userdataDir, ".cache", "ai-debug")
	if err := os.MkdirAll(debugDir, 0o755); err != nil {
		return err
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

	progress := newDirectiveProgressTable(a.Out)
	defer progress.Done()

	statuses, err := workflow.CollectStatuses(ctx, workflow.Deps{
		Registry:          a.Reg,
		Now:               a.Now,
		ExpandRepoPath:    expand,
		OnDirectiveUpdate: progress.Update,
	}, cfg, *userdataDir, resolved.Since, resolved.Until, collectors.Scope(resolved.Scope))
	if err != nil {
		return err
	}
	progress.Done()

	// Scope==self keeps only the configured user's activity (today's
	// daily-plan / recent-activity behavior). collector_error rows always
	// pass through FilterToSelf so failures stay visible. Other scopes
	// (repo, all) skip the filter; scope-aware collection inside each
	// collector is a follow-up effort.
	if resolved.Scope == executionmode.ScopeSelf {
		statuses = collectors.FilterToSelf(statuses)
	}

	// Per-run provider formatter override: SelectProvider picks formatter
	// from ai.activity_formatter; ExecutionMode may override it for this
	// run only.
	if resolved.Formatter != "" {
		provider = withFormatter(provider, ai.SelectActivityFormatter(resolved.Formatter))
	}

	md, err := provider.RunMode(ctx, ai.RunInput{
		ModeID:       resolved.ModeID,
		ModeName:     resolved.ModeName,
		Now:          resolved.Until,
		Since:        resolved.Since,
		LookbackDays: resolved.LookbackDays,
		Instruction:  resolved.Instruction,
		Statuses:     statuses,
		DebugDir:     debugDir,
		StreamOut:    stream,
	})
	if err != nil {
		return err
	}
	md = strings.TrimSpace(md) + "\n"

	fmt.Fprint(a.Out, md)

	if !*noSave {
		out := strings.TrimSpace(*outPath)
		if out == "" {
			if err := os.MkdirAll(filepath.Join(*userdataDir, "output"), 0o755); err != nil {
				return err
			}
			out = filepath.Join(*userdataDir, "output", fmt.Sprintf("%s-%s.md", outDate, resolved.ModeID))
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		out, err = uniqueOutputPath(out)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(a.Err, "Wrote %s\n", out)
	}

	return nil
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
	t.printTableLine("Collectors")
	t.printTableLine("Indicator | Task Description | Status")
	for _, id := range t.order {
		row := t.rows[id]
		t.printTableLine(fmt.Sprintf("%s | %s | %s", t.statusIcon(row.Status), row.Description, t.statusLabel(row)))
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
