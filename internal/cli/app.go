package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"github.com/kurt/slakkr-ai/internal/workflow"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Mode names for CLI and output filenames.
const (
	ModeDailyPlan      = "daily-plan"
	ModeRecentActivity = "recent-activity"
	ModeCustomPrompt   = "custom-prompt"
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
	modeFlag := fs.String("mode", "", "daily-plan | recent-activity | custom-prompt")
	outPath := fs.String("out", "", "output markdown path (default userdata/output/<date>-<mode>.md)")
	noSave := fs.Bool("no-save", false, "do not write output file")
	dateStr := fs.String("date", "", "date label YYYY-MM-DD for output filename (default today)")

	days := fs.Int("days", 0, "lookback days (recent-activity & custom-prompt; 0 = prompt or default 7)")
	priorities := fs.String("priorities", "", "today's priorities (daily-plan)")
	noPrompt := fs.Bool("no-prompt", false, "skip priorities prompt (daily-plan)")
	promptFlag := fs.String("prompt", "", "custom prompt text")
	promptFile := fs.String("prompt-file", "", "read custom prompt from file")

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

	mode := strings.TrimSpace(*modeFlag)
	if mode == "" {
		if !a.stdinIsTerminal() {
			return fmt.Errorf("--mode is required when stdin is not a TTY")
		}
		if err := survey.AskOne(&survey.Select{
			Message: "Choose mode",
			Options: []string{ModeDailyPlan, ModeRecentActivity, ModeCustomPrompt},
		}, &mode, survey.WithStdio(os.Stdin, os.Stdout, os.Stderr)); err != nil {
			return err
		}
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
	expand := expandRepoPathFromStdin(a.In)

	until := a.Now()
	var since time.Time
	var userPriorities string
	var userPrompt string
	lookbackDays := *days

	switch mode {
	case ModeDailyPlan:
		since = PreviousWeekdayStart(until)
		if !*noPrompt && a.stdinIsTerminal() {
			p := strings.TrimSpace(*priorities)
			if p == "" {
				prompter := StdioPrompter{In: a.In, Out: a.Out}
				var err error
				p, err = prompter.Ask("Anything you want to prioritize today? (Enter to skip)", "")
				if err != nil {
					return err
				}
			}
			userPriorities = strings.TrimSpace(p)
		} else {
			userPriorities = strings.TrimSpace(*priorities)
		}

	case ModeRecentActivity:
		if lookbackDays <= 0 {
			if a.stdinIsTerminal() {
				prompter := StdioPrompter{In: a.In, Out: a.Out}
				v, err := prompter.Ask("Lookback days", "7")
				if err != nil {
					return err
				}
				v = strings.TrimSpace(v)
				if v == "" {
					lookbackDays = 7
				} else {
					n, err := strconv.Atoi(v)
					if err != nil || n < 1 {
						return fmt.Errorf("lookback days must be a positive integer")
					}
					lookbackDays = n
				}
			} else {
				lookbackDays = 7
			}
		}
		since = lookbackSince(until, lookbackDays)

	case ModeCustomPrompt:
		if lookbackDays <= 0 {
			if a.stdinIsTerminal() {
				prompter := StdioPrompter{In: a.In, Out: a.Out}
				v, err := prompter.Ask("Lookback days", "7")
				if err != nil {
					return err
				}
				v = strings.TrimSpace(v)
				if v == "" {
					lookbackDays = 7
				} else {
					n, err := strconv.Atoi(v)
					if err != nil || n < 1 {
						return fmt.Errorf("lookback days must be a positive integer")
					}
					lookbackDays = n
				}
			} else {
				lookbackDays = 7
			}
		}
		since = lookbackSince(until, lookbackDays)
		userPrompt = strings.TrimSpace(*promptFlag)
		if pf := strings.TrimSpace(*promptFile); pf != "" {
			b, err := os.ReadFile(pf)
			if err != nil {
				return err
			}
			userPrompt = string(b)
		}
		if userPrompt == "" {
			if !a.stdinIsTerminal() {
				return fmt.Errorf("custom-prompt requires --prompt, --prompt-file, or a TTY")
			}
			prompter := StdioPrompter{In: a.In, Out: a.Out}
			var err error
			userPrompt, err = prompter.Ask("Your prompt for the model", "")
			if err != nil {
				return err
			}
		}
		if strings.TrimSpace(userPrompt) == "" {
			return fmt.Errorf("custom prompt is empty")
		}

	default:
		return fmt.Errorf("unknown mode %q (expected daily-plan, recent-activity, custom-prompt)", mode)
	}

	progress := newDirectiveProgressTable(a.Out)
	defer progress.Done()

	statuses, err := workflow.CollectStatuses(ctx, workflow.Deps{
		Registry:          a.Reg,
		Now:               a.Now,
		ExpandRepoPath:    expand,
		OnDirectiveUpdate: progress.Update,
	}, cfg, *userdataDir, since, until)
	if err != nil {
		return err
	}
	progress.Done()

	var md string
	switch mode {
	case ModeDailyPlan:
		in := ai.DailyPlanInput{
			Now:            until,
			Since:          since,
			UserPriorities: userPriorities,
			Statuses:       statuses,
			DebugDir:       debugDir,
			StreamOut:      stream,
		}
		md, err = provider.GenerateDailyPlan(ctx, in)
	case ModeRecentActivity:
		in := ai.RecentActivityInput{
			Now:          until,
			Since:        since,
			LookbackDays: lookbackDays,
			Statuses:     statuses,
			DebugDir:     debugDir,
			StreamOut:    stream,
		}
		md, err = provider.SummarizeRecentActivity(ctx, in)
	case ModeCustomPrompt:
		in := ai.CustomPromptInput{
			Now:          until,
			Since:        since,
			LookbackDays: lookbackDays,
			UserPrompt:   userPrompt,
			Statuses:     statuses,
			DebugDir:     debugDir,
			StreamOut:    stream,
		}
		md, err = provider.RunCustomPrompt(ctx, in)
	}
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
			out = filepath.Join(*userdataDir, "output", fmt.Sprintf("%s-%s.md", outDate, mode))
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(a.Err, "Wrote %s\n", out)
	}

	return nil
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
	if err := yaml.Unmarshal(content, &cfg); err != nil {
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
