package userdata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Store struct {
	Root      string
	GitClient GitClient
}

type GitClient interface {
	Init(ctx context.Context, dir string) error
	CommitAll(ctx context.Context, dir, message string) error
	AddRemote(ctx context.Context, dir, name, url string) error
	// GetRemoteURL returns the URL for the named remote (e.g. "origin") in repoDir, or "" if unavailable.
	GetRemoteURL(ctx context.Context, repoDir, remote string) (string, error)
}

type OSExecGit struct{}

func NewStore(root string) Store {
	return Store{Root: root, GitClient: OSExecGit{}}
}

func (s Store) Ensure(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Join(s.Root, "daybook"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.Root, "status-cache"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.Root, "plans"), 0o755); err != nil {
		return err
	}
	if err := writeDefaultYAML(filepath.Join(s.Root, "projects.yaml"), ProjectsFile{}); err != nil {
		return err
	}
	if err := writeDefaultYAML(filepath.Join(s.Root, "tasks.yaml"), TasksFile{}); err != nil {
		return err
	}
	if err := writeDefaultYAML(filepath.Join(s.Root, "directives.yaml"), DirectivesFile{}); err != nil {
		return err
	}
	if err := writeDefaultYAML(filepath.Join(s.Root, "config.yaml"), ConfigFile{
		Daybook: DaybookConfig{
			DefaultSections: []string{"Plan", "Status Updates", "Delegation Candidates", "Reflection"},
		},
		AI: AIConfig{Provider: "rule-based"},
	}); err != nil {
		return err
	}
	if err := writeDefaultYAML(filepath.Join(s.Root, "delegations.yaml"), DelegationsFile{}); err != nil {
		return err
	}
	if err := writeDefaultYAML(filepath.Join(s.Root, "signals.yaml"), SignalsFile{}); err != nil {
		return err
	}
	if err := writeDefaultYAML(filepath.Join(s.Root, "proposed-tasks.yaml"), ProposedTasksFile{}); err != nil {
		return err
	}
	if err := writeDefaultText(filepath.Join(s.Root, ".gitignore"), ".env\n"); err != nil {
		return err
	}
	if s.GitClient != nil {
		return s.GitClient.Init(ctx, s.Root)
	}
	return nil
}

func (s Store) LoadProjects() (ProjectsFile, error) {
	var file ProjectsFile
	err := readYAML(filepath.Join(s.Root, "projects.yaml"), &file)
	return file, err
}

func (s Store) SaveProjects(file ProjectsFile) error {
	if err := file.Validate(); err != nil {
		return err
	}
	return writeYAML(filepath.Join(s.Root, "projects.yaml"), file)
}

func (s Store) LoadTasks(projects ProjectsFile) (TasksFile, error) {
	var file TasksFile
	err := readYAML(filepath.Join(s.Root, "tasks.yaml"), &file)
	if err != nil {
		return file, err
	}
	return file, file.Validate(projects)
}

func (s Store) SaveTasks(projects ProjectsFile, file TasksFile) error {
	if err := file.Validate(projects); err != nil {
		return err
	}
	return writeYAML(filepath.Join(s.Root, "tasks.yaml"), file)
}

func (s Store) LoadDirectives(projects ProjectsFile) (DirectivesFile, error) {
	var file DirectivesFile
	err := readYAML(filepath.Join(s.Root, "directives.yaml"), &file)
	if err != nil {
		return file, err
	}
	return file, file.Validate(projects)
}

func (s Store) SaveDirectives(projects ProjectsFile, file DirectivesFile) error {
	if err := file.Validate(projects); err != nil {
		return err
	}
	return writeYAML(filepath.Join(s.Root, "directives.yaml"), file)
}

func (s Store) LoadConfig() (ConfigFile, error) {
	var file ConfigFile
	err := readYAML(filepath.Join(s.Root, "config.yaml"), &file)
	return file, err
}

func (s Store) SaveConfig(file ConfigFile) error {
	if err := file.Validate(); err != nil {
		return err
	}
	return writeYAML(filepath.Join(s.Root, "config.yaml"), file)
}

func (s Store) ValidateAll() error {
	projects, err := s.LoadProjects()
	if err != nil {
		return fmt.Errorf("load projects: %w", err)
	}
	if err := projects.Validate(); err != nil {
		return fmt.Errorf("validate projects: %w", err)
	}
	tasks, err := s.LoadTasks(projects)
	if err != nil {
		return fmt.Errorf("validate tasks: %w", err)
	}
	if _, err := s.LoadDirectives(projects); err != nil {
		return fmt.Errorf("validate directives: %w", err)
	}
	cfg, err := s.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	if _, err := s.LoadDelegations(tasks); err != nil {
		return fmt.Errorf("validate delegations: %w", err)
	}
	if _, err := s.LoadSignals(projects, tasks); err != nil {
		return fmt.Errorf("validate signals: %w", err)
	}
	if _, err := s.LoadProposedTasks(projects, tasks); err != nil {
		return fmt.Errorf("validate proposed tasks: %w", err)
	}
	return nil
}

func (s Store) LoadSignals(projects ProjectsFile, tasks TasksFile) (SignalsFile, error) {
	var file SignalsFile
	err := readYAML(filepath.Join(s.Root, "signals.yaml"), &file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SignalsFile{}, nil
		}
		return file, err
	}
	return file, file.ValidateWithProjects(projects, tasks)
}

func (s Store) SaveSignals(projects ProjectsFile, tasks TasksFile, file SignalsFile) error {
	if err := file.ValidateWithProjects(projects, tasks); err != nil {
		return err
	}
	return writeYAML(filepath.Join(s.Root, "signals.yaml"), file)
}

func (s Store) LoadProposedTasks(projects ProjectsFile, tasks TasksFile) (ProposedTasksFile, error) {
	var file ProposedTasksFile
	err := readYAML(filepath.Join(s.Root, "proposed-tasks.yaml"), &file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProposedTasksFile{}, nil
		}
		return file, err
	}
	return file, file.Validate(projects, tasks)
}

func (s Store) SaveProposedTasks(projects ProjectsFile, tasks TasksFile, file ProposedTasksFile) error {
	if err := file.Validate(projects, tasks); err != nil {
		return err
	}
	return writeYAML(filepath.Join(s.Root, "proposed-tasks.yaml"), file)
}

func (s Store) CommitAll(ctx context.Context, message string) error {
	if s.GitClient == nil {
		return nil
	}
	return s.GitClient.CommitAll(ctx, s.Root, message)
}

func (s Store) AddRemote(ctx context.Context, name, url string) error {
	if s.GitClient == nil || url == "" {
		return nil
	}
	return s.GitClient.AddRemote(ctx, s.Root, name, url)
}

func writeDefaultYAML(path string, value any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeYAML(path, value)
}

func writeDefaultText(path, value string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, []byte(value), 0o644)
}

func readYAML(path string, out any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	return yaml.Unmarshal(content, out)
}

func writeYAML(path string, value any) error {
	content, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func (OSExecGit) Init(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	return runGit(ctx, dir, "init", "--template=")
}

func (OSExecGit) CommitAll(ctx context.Context, dir, message string) error {
	if err := runGit(ctx, dir, "add", "."); err != nil {
		return err
	}
	status, err := gitOutput(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	return runGit(ctx, dir, "commit", "-m", message)
}

func (OSExecGit) AddRemote(ctx context.Context, dir, name, remoteURL string) error {
	remotes, err := gitOutput(ctx, dir, "remote")
	if err != nil {
		return err
	}
	for _, remote := range strings.Fields(remotes) {
		if remote == name {
			return runGit(ctx, dir, "remote", "set-url", name, remoteURL)
		}
	}
	return runGit(ctx, dir, "remote", "add", name, remoteURL)
}

func (OSExecGit) GetRemoteURL(ctx context.Context, repoDir, remote string) (string, error) {
	if strings.TrimSpace(repoDir) == "" {
		return "", nil
	}
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	out, err := gitOutput(ctx, repoDir, "remote", "get-url", remote)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := gitOutput(ctx, dir, args...)
	return err
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (s Store) LoadDelegations(tasks TasksFile) (DelegationsFile, error) {
	var file DelegationsFile
	path := filepath.Join(s.Root, "delegations.yaml")
	err := readYAML(path, &file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DelegationsFile{}, nil
		}
		return file, err
	}
	return file, file.Validate(tasks)
}

func (s Store) SaveDelegations(tasks TasksFile, file DelegationsFile) error {
	if err := file.Validate(tasks); err != nil {
		return err
	}
	return writeYAML(filepath.Join(s.Root, "delegations.yaml"), file)
}

func (s Store) DailyPlanPath(date time.Time) string {
	return filepath.Join(s.Root, "plans", date.Format("2006-01-02")+".yaml")
}

func (s Store) LoadDailyPlan(date time.Time) (DailyPlanFile, error) {
	var file DailyPlanFile
	err := readYAML(s.DailyPlanPath(date), &file)
	return file, err
}

func (s Store) SaveDailyPlan(file DailyPlanFile) error {
	if err := file.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.Root, "plans"), 0o755); err != nil {
		return err
	}
	date, err := time.Parse("2006-01-02", file.Date)
	if err != nil {
		return err
	}
	return writeYAML(s.DailyPlanPath(date), file)
}
