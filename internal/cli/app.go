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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/daybook"
	"github.com/kurt/slakkr-ai/internal/recipes"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"github.com/kurt/slakkr-ai/internal/web"
	"github.com/kurt/slakkr-ai/internal/workflow"
	"gopkg.in/yaml.v3"
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
	case "configure_host":
		return a.configureHost(ctx, args[1:])
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
	case "serve":
		return a.serve(ctx, args[1:])
	case "delegation":
		return a.delegation(ctx, args[1:])
	case "plan":
		return a.planCmd(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a *App) usage() error {
	fmt.Fprintln(a.Out, "Usage: slakkr <setup|configure_host|add_project|add_task|start_day|update_status|end_day|validate|list-recipes|serve|delegation|plan> [flags]")
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

func (a *App) configureHost(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("configure_host", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	hostFlag := fs.String("host", "", "host id (defaults to SLAKKR_HOST or hostname)")
	codeHomeFlag := fs.String("code-home", "", "default code home (defaults to CODE_HOME)")
	nonInteractive := fs.Bool("yes", false, "accept defaults")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store := userdata.NewStore(*userdataDir)
	store.GitClient = a.Git
	if err := store.Ensure(ctx); err != nil {
		return err
	}
	hostID := strings.TrimSpace(*hostFlag)
	if hostID == "" {
		var err error
		hostID, err = userdata.CurrentHostID()
		if err != nil {
			return err
		}
	} else {
		var err error
		hostID, err = userdata.SanitizeHostKey(hostID)
		if err != nil {
			return err
		}
	}
	projects, err := store.LoadProjects()
	if err != nil {
		return err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.Hosts == nil {
		cfg.Hosts = map[string]userdata.HostProfile{}
	}
	prompter := StdioPrompter{In: a.In, Out: a.Out}
	codeHome := strings.TrimSpace(*codeHomeFlag)
	if codeHome == "" {
		codeHome = strings.TrimSpace(os.Getenv(userdata.EnvCodeHome))
	}
	codeHome = expandRepoPath(codeHome)
	if !*nonInteractive {
		currentDefault := codeHome
		if currentDefault == "" {
			currentDefault = userdata.EffectiveCodeHome(cfg, hostID)
		}
		entered, err := prompter.Ask("Code home for host "+hostID, currentDefault)
		if err != nil {
			return err
		}
		codeHome = expandRepoPath(entered)
	}
	cfg.Hosts[hostID] = userdata.HostProfile{CodeHome: codeHome}
	if err := store.SaveConfig(cfg); err != nil {
		return err
	}

	importedCount := 0
	if codeHome != "" {
		dirs, err := immediateSubdirs(codeHome)
		if err != nil {
			return err
		}
		selected, err := selectFoldersForImport(dirs, projects, hostID, *nonInteractive, prompter, a.In, a.Out)
		if err != nil {
			return err
		}
		for _, dir := range dirs {
			if !selected[dir] {
				continue
			}
			if hostHasPath(projects, hostID, dir) {
				continue
			}
			project := importedProjectFromDir(projects, hostID, dir)
			upsertProject(&projects, project)
			importedCount++
		}
		if importedCount > 0 {
			if err := store.SaveProjects(projects); err != nil {
				return err
			}
		}
	}
	if err := store.CommitAll(ctx, "Configure slakkr host "+hostID); err != nil {
		fmt.Fprintf(a.Err, "warning: could not commit userdata: %v\n", err)
	}
	fmt.Fprintf(a.Out, "Configured host %s.\n", hostID)
	if codeHome != "" {
		fmt.Fprintf(a.Out, "Code home: %s\n", codeHome)
	}
	fmt.Fprintf(a.Out, "Imported %d project(s).\n", importedCount)
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

func immediateSubdirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		out = append(out, filepath.Join(root, entry.Name()))
	}
	sort.Strings(out)
	return out, nil
}

func selectFoldersForImport(
	dirs []string,
	projects userdata.ProjectsFile,
	hostID string,
	nonInteractive bool,
	prompter Prompter,
	in io.Reader,
	out io.Writer,
) (map[string]bool, error) {
	selected := map[string]bool{}
	if len(dirs) == 0 {
		return selected, nil
	}
	if nonInteractive {
		for _, dir := range dirs {
			if hostHasPath(projects, hostID, dir) {
				selected[dir] = true
			}
		}
		return selected, nil
	}
	if in == os.Stdin && out == os.Stdout {
		var options []string
		dirByOption := map[string]string{}
		var defaults []string
		for _, dir := range dirs {
			label := fmt.Sprintf("%s (%s)", filepath.Base(dir), dir)
			options = append(options, label)
			dirByOption[label] = dir
			if hostHasPath(projects, hostID, dir) {
				defaults = append(defaults, label)
			}
		}
		var chosen []string
		prompt := &survey.MultiSelect{
			Message: "Select folders to import as projects",
			Options: options,
			Default: defaults,
		}
		if err := survey.AskOne(prompt, &chosen, survey.WithStdio(os.Stdin, os.Stdout, os.Stderr)); err != nil {
			return nil, err
		}
		for _, option := range chosen {
			if dir := dirByOption[option]; dir != "" {
				selected[dir] = true
			}
		}
		return selected, nil
	}
	// Fallback for tests or non-terminal stdin: prompt one directory at a time.
	for _, dir := range dirs {
		alreadyTracked := hostHasPath(projects, hostID, dir)
		prompt := fmt.Sprintf("Import project %s", filepath.Base(dir))
		choice, err := prompter.Confirm(prompt, alreadyTracked)
		if err != nil {
			return nil, err
		}
		if choice {
			selected[dir] = true
		}
	}
	return selected, nil
}

func hostHasPath(projects userdata.ProjectsFile, hostID, path string) bool {
	path = expandRepoPath(path)
	for _, project := range projects.Projects {
		for _, repo := range project.Repos {
			for _, existing := range userdata.RepoWorktreePaths(repo, hostID) {
				if expandRepoPath(existing) == path {
					return true
				}
			}
		}
	}
	return false
}

func importedProjectFromDir(projects userdata.ProjectsFile, hostID, dir string) userdata.Project {
	name := filepath.Base(dir)
	id := slugID(name)
	project := userdata.Project{
		ID:          id,
		Name:        name,
		Description: "Imported from code home",
		Repos: []userdata.Repo{{
			ID:          id,
			Name:        name,
			PathsByHost: map[string][]string{hostID: {expandRepoPath(dir)}},
		}},
	}
	if idx := projectIndexByID(projects.Projects, id); idx >= 0 {
		project = mergeProjectRepos(projects.Projects[idx], project, hostID)
	}
	return project
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
	store := userdata.NewStore(*userdataDir)
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	provider := ai.SelectProvider(cfg.AI, a.AI)
	output, err := provider.ProposeDayPlan(ctx, input)
	if err != nil {
		return err
	}
	ai.NormalizePlanningOutput(&output)
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
	planFile := workflow.DailyPlanFromOutput(input.Date, output, a.Now())
	if err := store.SaveDailyPlan(planFile); err != nil {
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
	store := userdata.NewStore(*userdataDir)
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	provider := ai.SelectProvider(cfg.AI, a.AI)
	output, err := provider.ReflectEndOfDay(ctx, input)
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
	return workflow.BuildPlanningInput(ctx, workflow.Deps{
		Registry:       a.Registry,
		Now:            a.Now,
		ExpandRepoPath: expandRepoPath,
	}, userdataDir, date, mode)
}

func (a *App) serve(ctx context.Context, args []string) error {
	_ = ctx
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	addr := fs.String("addr", "127.0.0.1:8765", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv := web.Server{
		UserdataDir: *userdataDir,
		Deps: workflow.Deps{
			Registry:       a.Registry,
			Now:            a.Now,
			ExpandRepoPath: expandRepoPath,
		},
	}
	fmt.Fprintf(a.Out, "Listening on http://%s (userdata=%s)\n", *addr, *userdataDir)
	return srv.ListenAndServe(*addr)
}

func (a *App) delegation(ctx context.Context, args []string) error {
	_ = ctx
	if len(args) < 1 {
		return fmt.Errorf("usage: delegation <list|add> [flags]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("delegation_list", flag.ContinueOnError)
		fs.SetOutput(a.Err)
		userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		store := userdata.NewStore(*userdataDir)
		projects, err := store.LoadProjects()
		if err != nil {
			return err
		}
		tasks, err := store.LoadTasks(projects)
		if err != nil {
			return err
		}
		del, err := store.LoadDelegations(tasks)
		if err != nil {
			return err
		}
		for _, e := range del.Delegations {
			fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", e.ID, e.State, e.TaskID, e.Title)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("delegation_add", flag.ContinueOnError)
		fs.SetOutput(a.Err)
		userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
		id := fs.String("id", "", "delegation id (generated if empty)")
		title := fs.String("title", "", "title")
		state := fs.String("state", string(userdata.AgentWorkCandidate), "state")
		taskID := fs.String("task-id", "", "optional task id")
		prompt := fs.String("prompt", "", "prompt for the agent")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *title == "" {
			return fmt.Errorf("--title is required")
		}
		st := userdata.AgentWorkState(*state)
		if !validAgentDelegationState(st) {
			return fmt.Errorf("invalid --state %q", *state)
		}
		genID := *id
		if genID == "" {
			genID = fmt.Sprintf("ag-%d", a.Now().Unix())
		}
		store := userdata.NewStore(*userdataDir)
		projects, err := store.LoadProjects()
		if err != nil {
			return err
		}
		tasks, err := store.LoadTasks(projects)
		if err != nil {
			return err
		}
		entry := userdata.AgentWorkEntry{
			ID:        genID,
			TaskID:    *taskID,
			State:     st,
			Title:     *title,
			Prompt:    *prompt,
			CreatedAt: userdata.YAMLDateTime{Time: a.Now().UTC()},
		}
		del, err := store.LoadDelegations(tasks)
		if err != nil {
			return err
		}
		del.Delegations = append(del.Delegations, entry)
		return store.SaveDelegations(tasks, del)
	default:
		return fmt.Errorf("unknown delegation subcommand %q", args[0])
	}
}

func validAgentDelegationState(s userdata.AgentWorkState) bool {
	switch s {
	case userdata.AgentWorkCandidate, userdata.AgentWorkReady, userdata.AgentWorkActive,
		userdata.AgentWorkNeedsReview, userdata.AgentWorkAccepted, userdata.AgentWorkRejected, userdata.AgentWorkSuperseded:
		return true
	default:
		return false
	}
}

func (a *App) planCmd(ctx context.Context, args []string) error {
	_ = ctx
	if len(args) < 1 || args[0] != "show" {
		fmt.Fprintln(a.Err, "usage: plan show [--userdata dir] [--date YYYY-MM-DD]")
		return nil
	}
	fs := flag.NewFlagSet("plan_show", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	userdataDir := fs.String("userdata", userdata.DefaultDir, "userdata directory")
	dateStr := fs.String("date", a.Now().Format("2006-01-02"), "plan date")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	date, err := time.Parse("2006-01-02", *dateStr)
	if err != nil {
		return err
	}
	store := userdata.NewStore(*userdataDir)
	plan, err := store.LoadDailyPlan(date)
	if err != nil {
		return err
	}
	if plan.Date == "" {
		return fmt.Errorf("no plan file for %s (run start_day)", *dateStr)
	}
	raw, err := yaml.Marshal(plan)
	if err != nil {
		return err
	}
	fmt.Fprint(a.Out, string(raw))
	return nil
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
