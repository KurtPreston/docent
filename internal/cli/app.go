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
	case "add_project":
		return a.addProject(ctx, args[1:])
	case "add_task":
		return a.addTask(ctx, args[1:])
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
	fmt.Fprintln(a.Out, "Usage: slakkr <setup|add_project|add_task|start_day|update_status|end_day|validate|list-recipes> [flags]")
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
	prompter := StdioPrompter{In: a.In, Out: a.Out}
	if *remote == "" && !*nonInteractive {
		addRemote, err := prompter.Confirm("Add a git remote for userdata persistence?", false)
		if err != nil {
			return err
		}
		if addRemote {
			remoteURL, err := prompter.Ask("Userdata git remote URL", "")
			if err != nil {
				return err
			}
			*remote = remoteURL
		}
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
	if err := a.printSetupNextSteps(store); err != nil {
		return err
	}
	if err := store.CommitAll(ctx, "Initialize slakkr userdata"); err != nil {
		fmt.Fprintf(a.Err, "warning: could not commit userdata: %v\n", err)
	}
	return nil
}

func (a *App) printSetupNextSteps(store userdata.Store) error {
	projects, err := store.LoadProjects()
	if err != nil {
		return err
	}
	tasks, err := store.LoadTasks(projects)
	if err != nil {
		return err
	}
	if len(projects.Projects) == 0 {
		fmt.Fprintln(a.Out, "No projects configured yet. Next: scripts/add_project")
	}
	if len(tasks.Tasks) == 0 {
		fmt.Fprintln(a.Out, "No tasks configured yet. Next: scripts/add_task")
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

// expandRepoPath turns a user-entered path (including "~/...") into an absolute path for tooling like git.
func expandRepoPath(path string) string {
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

func (a *App) addProject(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("add_project", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	id := fs.String("id", "", "project id")
	name := fs.String("name", "", "project name")
	description := fs.String("description", "", "project description")
	repoPath := fs.String("repo-path", "", "local repository path")
	repoRemote := fs.String("repo-remote", "", "repository remote URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompter := StdioPrompter{In: a.In, Out: a.Out}
	if *name == "" {
		value, err := prompter.Ask("Project name", "")
		if err != nil {
			return err
		}
		*name = value
	}
	if *name == "" {
		return skippedDirectiveError{reason: "project name was blank"}
	}
	if *id == "" {
		*id = slugID(*name)
	}
	if *description == "" {
		value, err := prompter.Ask("Project description", "")
		if err != nil {
			return err
		}
		*description = value
	}
	if *repoPath == "" {
		value, err := prompter.Ask("Local repo path (optional)", "")
		if err != nil {
			return err
		}
		*repoPath = value
	}
	remoteDefault := *repoRemote
	if remoteDefault == "" && a.Git != nil {
		if origin, err := a.Git.GetRemoteURL(ctx, expandRepoPath(*repoPath), "origin"); err == nil && origin != "" {
			remoteDefault = origin
		}
	}
	if *repoRemote == "" {
		value, err := prompter.Ask("Repo remote URL (optional)", remoteDefault)
		if err != nil {
			return err
		}
		*repoRemote = value
	}
	store := userdata.NewStore(*userdataDir)
	store.GitClient = a.Git
	if err := store.Ensure(ctx); err != nil {
		return err
	}
	projects, err := store.LoadProjects()
	if err != nil {
		return err
	}
	hostID, err := userdata.CurrentHostID()
	if err != nil {
		return err
	}
	project := userdata.Project{
		ID:          *id,
		Name:        *name,
		Description: *description,
	}
	repoID := slugID(*name)
	if *repoPath != "" || *repoRemote != "" {
		absPath := expandRepoPath(*repoPath)
		var pathsByHost map[string][]string
		if absPath != "" {
			pathsByHost = map[string][]string{hostID: {absPath}}
		}
		project.Repos = []userdata.Repo{{
			ID:          repoID,
			Name:        *name,
			Remote:      *repoRemote,
			PathsByHost: pathsByHost,
		}}
	}
	if idx := projectIndexByID(projects.Projects, project.ID); idx >= 0 && len(project.Repos) > 0 {
		project = mergeProjectRepos(projects.Projects[idx], project, hostID)
	}
	upsertProject(&projects, project)
	if err := store.SaveProjects(projects); err != nil {
		return err
	}
	if err := store.CommitAll(ctx, "Add slakkr project "+project.ID); err != nil {
		fmt.Fprintf(a.Err, "warning: could not commit userdata: %v\n", err)
	}
	fmt.Fprintf(a.Out, "Saved project %s.\n", project.ID)
	return nil
}

func (a *App) addTask(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("add_task", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	id := fs.String("id", "", "task id")
	projectID := fs.String("project", "", "project id")
	name := fs.String("name", "", "task name")
	description := fs.String("description", "", "task description")
	status := fs.String("status", string(userdata.TaskStatusReady), "task status")
	priority := fs.String("priority", string(userdata.PriorityMedium), "task priority")
	nextAction := fs.String("next-action", "", "next action")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store := userdata.NewStore(*userdataDir)
	store.GitClient = a.Git
	if err := store.Ensure(ctx); err != nil {
		return err
	}
	projects, err := store.LoadProjects()
	if err != nil {
		return err
	}
	if len(projects.Projects) == 0 {
		return fmt.Errorf("no projects configured yet; run scripts/add_project first")
	}
	prompter := StdioPrompter{In: a.In, Out: a.Out}
	if *projectID == "" {
		*projectID = projects.Projects[0].ID
		value, err := prompter.Ask("Project id", *projectID)
		if err != nil {
			return err
		}
		*projectID = value
	}
	if *name == "" {
		value, err := prompter.Ask("Task name", "")
		if err != nil {
			return err
		}
		*name = value
	}
	if *name == "" {
		return fmt.Errorf("task name is required")
	}
	if *id == "" {
		*id = slugID(*name)
	}
	if *description == "" {
		value, err := prompter.Ask("Task description", "")
		if err != nil {
			return err
		}
		*description = value
	}
	if *nextAction == "" {
		value, err := prompter.Ask("Next action", "")
		if err != nil {
			return err
		}
		*nextAction = value
	}
	tasks, err := store.LoadTasks(projects)
	if err != nil {
		return err
	}
	task := userdata.Task{
		ID:          *id,
		ProjectID:   *projectID,
		Name:        *name,
		Description: *description,
		Status:      userdata.TaskStatus(*status),
		Priority:    userdata.Priority(*priority),
		NextAction:  *nextAction,
		UpdatedAt:   userdata.YAMLDateTime{Time: a.Now()},
	}
	upsertTask(&tasks, task)
	if err := store.SaveTasks(projects, tasks); err != nil {
		return err
	}
	if err := store.CommitAll(ctx, "Add slakkr task "+task.ID); err != nil {
		fmt.Fprintf(a.Err, "warning: could not commit userdata: %v\n", err)
	}
	fmt.Fprintf(a.Out, "Saved task %s.\n", task.ID)
	return nil
}

func upsertProject(projects *userdata.ProjectsFile, project userdata.Project) {
	for i, existing := range projects.Projects {
		if existing.ID == project.ID {
			projects.Projects[i] = project
			return
		}
	}
	projects.Projects = append(projects.Projects, project)
}

func upsertTask(tasks *userdata.TasksFile, task userdata.Task) {
	for i, existing := range tasks.Tasks {
		if existing.ID == task.ID {
			tasks.Tasks[i] = task
			return
		}
	}
	tasks.Tasks = append(tasks.Tasks, task)
}

func projectIndexByID(projects []userdata.Project, id string) int {
	for i, p := range projects {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func mergeProjectRepos(existing, incoming userdata.Project, hostID string) userdata.Project {
	if len(incoming.Repos) == 0 {
		incoming.Repos = existing.Repos
		return incoming
	}
	merged := append([]userdata.Repo(nil), existing.Repos...)
	for _, inc := range incoming.Repos {
		idx := repoIndexByID(merged, inc.ID)
		if idx < 0 {
			merged = append(merged, inc)
			continue
		}
		merged[idx] = mergeRepoForHost(merged[idx], inc, hostID)
	}
	incoming.Repos = merged
	return incoming
}

func repoIndexByID(repos []userdata.Repo, id string) int {
	for i, r := range repos {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func mergeRepoForHost(existing, incoming userdata.Repo, hostID string) userdata.Repo {
	out := existing
	if strings.TrimSpace(incoming.Remote) != "" {
		out.Remote = incoming.Remote
	}
	if strings.TrimSpace(incoming.Name) != "" {
		out.Name = incoming.Name
	}
	if out.PathsByHost == nil {
		out.PathsByHost = map[string][]string{}
	}
	var add []string
	if incoming.PathsByHost != nil {
		add = append(add, incoming.PathsByHost[hostID]...)
	}
	if len(add) > 0 {
		out.PathsByHost[hostID] = dedupeStringKeepOrder(append(out.PathsByHost[hostID], add...))
	}
	return out
}

func dedupeStringKeepOrder(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
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
	cfg, err := store.LoadConfig()
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	hostID, err := userdata.CurrentHostID()
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	collectOpts := &collectors.CollectOpts{
		HostID:         hostID,
		Projects:       projects,
		Config:         cfg,
		ExpandRepoPath: expandRepoPath,
	}
	statuses, err := a.Registry.Collect(ctx, directives.Directives, collectOpts)
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
