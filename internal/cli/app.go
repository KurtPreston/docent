package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/daybook"
	"github.com/kurt/slakkr-ai/internal/recipes"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

type App struct {
	Out      io.Writer
	Err      io.Writer
	In       io.Reader
	Now      func() time.Time
	AI       ai.Provider
	Registry *collectors.Registry
	Git      userdata.GitClient
}

func New(out, errOut io.Writer, in io.Reader) *App {
	now := time.Now
	return &App{
		Out:      out,
		Err:      errOut,
		In:       in,
		Now:      now,
		AI:       ai.RuleBasedProvider{},
		Registry: collectors.NewRegistry(now),
		Git:      userdata.OSExecGit{},
	}
}

func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.usage()
	}
	switch args[0] {
	case "setup":
		return a.setup(ctx, args[1:])
	case "start_day":
		return a.startDay(ctx, args[1:])
	case "update_status":
		return a.updateStatus(ctx, args[1:])
	case "end_day":
		return a.endDay(ctx, args[1:])
	case "validate":
		return a.validate(args[1:])
	case "list-recipes":
		return a.listRecipes(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a *App) usage() error {
	fmt.Fprintln(a.Out, "Usage: slakkr <setup|start_day|update_status|end_day|validate|list-recipes> [flags]")
	return nil
}

func (a *App) setup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	recipesDir := fs.String("recipes", "recipes", "recipes directory")
	remote := fs.String("remote", "", "optional git remote URL for userdata")
	nonInteractive := fs.Bool("yes", false, "accept defaults")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store := userdata.NewStore(*userdataDir)
	store.GitClient = a.Git
	if err := store.Ensure(ctx); err != nil {
		return err
	}
	if *remote != "" {
		if err := store.AddRemote(ctx, "origin", *remote); err != nil {
			return err
		}
	}
	loaded, err := recipes.LoadDir(*recipesDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Initialized %s and found %d recipe(s).\n", *userdataDir, len(loaded))
	prompter := StdioPrompter{In: a.In, Out: a.Out}
	if !*nonInteractive && len(loaded) > 0 {
		for _, recipe := range loaded {
			enable, err := prompter.Confirm("Enable recipe "+recipe.ID+"?", false)
			if err != nil {
				return err
			}
			if !enable {
				continue
			}
			directive, err := promptDirective(prompter, recipe)
			if err != nil {
				return err
			}
			if err := addDirective(store, directive); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "Saved directive %s.\n", directive.ID)
		}
	}
	if err := store.CommitAll(ctx, "Initialize slakkr userdata"); err != nil {
		fmt.Fprintf(a.Err, "warning: could not commit userdata: %v\n", err)
	}
	return nil
}

func promptDirective(prompter Prompter, recipe recipes.Recipe) (userdata.Directive, error) {
	id, err := prompter.Ask("Directive id", recipe.ID)
	if err != nil {
		return userdata.Directive{}, err
	}
	name, err := prompter.Ask("Directive name", recipe.Name)
	if err != nil {
		return userdata.Directive{}, err
	}
	config := map[string]string{}
	for _, field := range recipe.RequiredConfig {
		if field.Secret {
			continue
		}
		value, err := prompter.Ask("Config "+field.Name, recipe.Defaults.Config[field.Name])
		if err != nil {
			return userdata.Directive{}, err
		}
		config[field.Name] = value
	}
	target := map[string]string{}
	for _, field := range recipe.RequiredTarget {
		value, err := prompter.Ask("Target "+field.Name, recipe.Defaults.Target[field.Name])
		if err != nil {
			return userdata.Directive{}, err
		}
		target[field.Name] = value
	}
	return recipe.Instantiate(recipes.InstantiateInput{
		ID:      id,
		Name:    name,
		Config:  config,
		Target:  target,
		Enabled: true,
	})
}

func addDirective(store userdata.Store, directive userdata.Directive) error {
	projects, err := store.LoadProjects()
	if err != nil {
		return err
	}
	directives, err := store.LoadDirectives(projects)
	if err != nil {
		return err
	}
	for i, existing := range directives.Directives {
		if existing.ID == directive.ID {
			directives.Directives[i] = directive
			return store.SaveDirectives(projects, directives)
		}
	}
	directives.Directives = append(directives.Directives, directive)
	return store.SaveDirectives(projects, directives)
}

func (a *App) startDay(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("start_day", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	date := fs.String("date", a.Now().Format("2006-01-02"), "daybook date")
	if err := fs.Parse(args); err != nil {
		return err
	}
	input, entry, err := a.planningInput(ctx, *userdataDir, *date, "start_day")
	if err != nil {
		return err
	}
	output, err := a.AI.ProposeDayPlan(ctx, input)
	if err != nil {
		return err
	}
	if err := daybook.AppendStatus(entry, input.Statuses); err != nil {
		return err
	}
	entry, err = daybook.LoadOrCreate(*userdataDir, input.Date)
	if err != nil {
		return err
	}
	if err := daybook.AppendPlan(entry, output); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Updated %s with %d status item(s).\n", entry.Path, len(input.Statuses))
	return nil
}

func (a *App) updateStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update_status", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	date := fs.String("date", a.Now().Format("2006-01-02"), "daybook date")
	if err := fs.Parse(args); err != nil {
		return err
	}
	input, entry, err := a.planningInput(ctx, *userdataDir, *date, "update_status")
	if err != nil {
		return err
	}
	if err := daybook.AppendStatus(entry, input.Statuses); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Updated %s with %d status item(s).\n", entry.Path, len(input.Statuses))
	return nil
}

func (a *App) endDay(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("end_day", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	date := fs.String("date", a.Now().Format("2006-01-02"), "daybook date")
	if err := fs.Parse(args); err != nil {
		return err
	}
	input, entry, err := a.planningInput(ctx, *userdataDir, *date, "end_day")
	if err != nil {
		return err
	}
	output, err := a.AI.ReflectEndOfDay(ctx, input)
	if err != nil {
		return err
	}
	if err := daybook.AppendStatus(entry, input.Statuses); err != nil {
		return err
	}
	entry, err = daybook.LoadOrCreate(*userdataDir, input.Date)
	if err != nil {
		return err
	}
	if err := daybook.AppendReflection(entry, output); err != nil {
		return err
	}
	store := userdata.NewStore(*userdataDir)
	if err := store.CommitAll(ctx, "Update slakkr daybook for "+input.Date.Format("2006-01-02")); err != nil {
		fmt.Fprintf(a.Err, "warning: could not commit userdata: %v\n", err)
	}
	fmt.Fprintf(a.Out, "Finalized %s.\n", entry.Path)
	return nil
}

func (a *App) planningInput(ctx context.Context, userdataDir, dateString, mode string) (ai.PlanningInput, daybook.Entry, error) {
	date, err := time.Parse("2006-01-02", dateString)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	store := userdata.NewStore(userdataDir)
	projects, err := store.LoadProjects()
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	tasks, err := store.LoadTasks(projects)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	directives, err := store.LoadDirectives(projects)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	statuses, err := a.Registry.Collect(ctx, directives.Directives)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	entry, err := daybook.LoadOrCreate(userdataDir, date)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	return ai.PlanningInput{
		Date:     date,
		Projects: projects.Projects,
		Tasks:    tasks.Tasks,
		Statuses: statuses,
		Daybook:  entry.Content,
		Mode:     mode,
	}, entry, nil
}

func (a *App) validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store := userdata.NewStore(*userdataDir)
	if err := store.ValidateAll(); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "%s is valid.\n", *userdataDir)
	return nil
}

func (a *App) listRecipes(args []string) error {
	fs := flag.NewFlagSet("list-recipes", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	recipesDir := fs.String("recipes", "recipes", "recipes directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	loaded, err := recipes.LoadDir(*recipesDir)
	if err != nil {
		return err
	}
	for _, recipe := range loaded {
		fmt.Fprintf(a.Out, "%s\t%s\t%s\n", recipe.ID, recipe.Collector, recipe.Name)
	}
	return nil
}

func ScriptName(command string) string {
	return filepath.Join("scripts", strings.ReplaceAll(command, "_", "_"))
}

func Main() {
	app := New(os.Stdout, os.Stderr, os.Stdin)
	if err := app.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
