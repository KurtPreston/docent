package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
			directive, err := promptDirective(prompter, recipe, *userdataDir)
			if err != nil {
				if errors.Is(err, errSkippedDirective) {
					fmt.Fprintf(a.Out, "Skipped recipe %s: %v\n", recipe.ID, err)
					continue
				}
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

var errSkippedDirective = errors.New("skipped directive")

type skippedDirectiveError struct {
	reason string
}

func (e skippedDirectiveError) Error() string {
	return e.reason
}

func (e skippedDirectiveError) Is(target error) bool {
	return target == errSkippedDirective
}

func promptDirective(prompter Prompter, recipe recipes.Recipe, userdataDir string) (userdata.Directive, error) {
	name, err := prompter.Ask(namePrompt(recipe), recipe.Name)
	if err != nil {
		return userdata.Directive{}, err
	}
	id := slugID(name)
	if name == recipe.Name {
		id = recipe.ID
	}
	config := map[string]string{}
	credentialRefs := map[string]string{}
	for _, field := range recipe.RequiredConfig {
		if field.Secret {
			envName := envNameForSecret(id, field.Name)
			value, err := prompter.Ask(fieldPrompt("Secret", field.Name, field.Description)+"; saved to userdata/.env as "+envName, "")
			if err != nil {
				return userdata.Directive{}, err
			}
			if field.Required && value == "" {
				return userdata.Directive{}, skippedDirectiveError{reason: "required secret " + field.Name + " was blank"}
			}
			if value != "" {
				if err := upsertEnvValue(filepath.Join(userdataDir, ".env"), envName, value); err != nil {
					return userdata.Directive{}, err
				}
			}
			credentialRefs[field.Name] = envName
			continue
		}
		value, err := prompter.Ask(fieldPrompt("Config", field.Name, field.Description), recipe.Defaults.Config[field.Name])
		if err != nil {
			return userdata.Directive{}, err
		}
		if field.Required && value == "" {
			return userdata.Directive{}, skippedDirectiveError{reason: "required config " + field.Name + " was blank"}
		}
		config[field.Name] = value
	}
	target := map[string]string{}
	for _, field := range recipe.RequiredTarget {
		value, err := prompter.Ask(fieldPrompt("Target", field.Name, field.Description), recipe.Defaults.Target[field.Name])
		if err != nil {
			return userdata.Directive{}, err
		}
		if field.Required && value == "" {
			return userdata.Directive{}, skippedDirectiveError{reason: "required target " + field.Name + " was blank"}
		}
		target[field.Name] = value
	}
	return recipe.Instantiate(recipes.InstantiateInput{
		ID:             id,
		Name:           name,
		Config:         config,
		Target:         target,
		CredentialRefs: credentialRefs,
		Enabled:        true,
	})
}

func namePrompt(recipe recipes.Recipe) string {
	switch recipe.Collector {
	case "gitea":
		return "Gitea server name"
	case "github-activity":
		return "GitHub account name"
	default:
		return "Name for this status check"
	}
}

func fieldPrompt(kind, name, description string) string {
	if description == "" {
		return kind + " " + name
	}
	return fmt.Sprintf("%s %s (%s)", kind, name, description)
}

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)
var envNamePattern = regexp.MustCompile(`[^A-Z0-9]+`)

func slugID(value string) string {
	slug := strings.ToLower(strings.TrimSpace(value))
	slug = slugPattern.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "directive"
	}
	if slug[0] < 'a' || slug[0] > 'z' {
		slug = "directive-" + slug
	}
	return slug
}

func envNameForSecret(directiveID, fieldName string) string {
	raw := strings.ToUpper(directiveID + "_" + fieldName)
	raw = envNamePattern.ReplaceAllString(raw, "_")
	raw = strings.Trim(raw, "_")
	if raw == "" {
		raw = "SECRET"
	}
	return "SLAKKR_" + raw
}

func upsertEnvValue(path, key, value string) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	line := key + "=" + strconv.Quote(value)
	var lines []string
	replaced := false
	for _, existing := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
		if existing == "" {
			continue
		}
		if strings.HasPrefix(existing, key+"=") {
			lines = append(lines, line)
			replaced = true
			continue
		}
		lines = append(lines, existing)
	}
	if !replaced {
		lines = append(lines, line)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
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
