package userdata

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const DefaultDir = "userdata"

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type ProjectsFile struct {
	Projects []Project `yaml:"projects"`
}

type Project struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Links       []Link   `yaml:"links,omitempty"`
	Repos       []Repo   `yaml:"repos,omitempty"`
	Context     []string `yaml:"context,omitempty"`
}

type Repo struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name,omitempty"`
	// PathsByHost is working tree locations per host id (see SLAKKR_HOST / CurrentHostID).
	PathsByHost map[string][]string `yaml:"paths_by_host,omitempty"`
	Remote      string              `yaml:"remote,omitempty"`
	Host        string              `yaml:"host,omitempty"`
}

type TasksFile struct {
	Tasks []Task `yaml:"tasks"`
}

type Task struct {
	ID          string       `yaml:"id"`
	ProjectID   string       `yaml:"project_id"`
	Name        string       `yaml:"name"`
	Description string       `yaml:"description,omitempty"`
	Status      TaskStatus   `yaml:"status"`
	Priority    Priority     `yaml:"priority"`
	Links       []Link       `yaml:"links,omitempty"`
	NextAction  string       `yaml:"next_action,omitempty"`
	Delegation  Delegation   `yaml:"delegation,omitempty"`
	UpdatedAt   YAMLDateTime `yaml:"updated_at,omitempty"`
}

type TaskStatus string

const (
	TaskStatusBacklog    TaskStatus = "backlog"
	TaskStatusReady      TaskStatus = "ready"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusBlocked    TaskStatus = "blocked"
	TaskStatusDone       TaskStatus = "done"
	TaskStatusDropped    TaskStatus = "dropped"
)

type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityMedium   Priority = "medium"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

type Delegation struct {
	State           DelegationState `yaml:"state,omitempty"`
	Reason          string          `yaml:"reason,omitempty"`
	SuggestedPrompt string          `yaml:"suggested_prompt,omitempty"`
}

type DelegationState string

const (
	DelegationUnknown     DelegationState = ""
	DelegationCandidate   DelegationState = "candidate"
	DelegationActive      DelegationState = "active"
	DelegationNotSuitable DelegationState = "not_suitable"
)

type Link struct {
	Type  string `yaml:"type"`
	URL   string `yaml:"url"`
	Title string `yaml:"title,omitempty"`
}

type DirectivesFile struct {
	Directives []Directive `yaml:"directives"`
}

type Directive struct {
	ID             string            `yaml:"id"`
	RecipeID       string            `yaml:"recipe_id,omitempty"`
	Name           string            `yaml:"name"`
	Collector      string            `yaml:"collector"`
	Enabled        bool              `yaml:"enabled"`
	Schedule       string            `yaml:"schedule,omitempty"`
	ProjectID      string            `yaml:"project_id,omitempty"`
	Target         map[string]string `yaml:"target,omitempty"`
	Config         map[string]string `yaml:"config,omitempty"`
	CredentialRefs map[string]string `yaml:"credential_refs,omitempty"`
	SummaryPrompt  string            `yaml:"summary_prompt,omitempty"`
}

type DaybookConfig struct {
	Timezone        string   `yaml:"timezone,omitempty"`
	DefaultSections []string `yaml:"default_sections,omitempty"`
}

// HostProfile holds machine-local settings referenced by a host id key (SLAKKR_HOST or sanitized hostname).
type HostProfile struct {
	CodeHome string `yaml:"code_home,omitempty"`
}

type ConfigFile struct {
	Daybook DaybookConfig          `yaml:"daybook"`
	Hosts   map[string]HostProfile `yaml:"hosts,omitempty"`
	AI      AIConfig               `yaml:"ai,omitempty"`
}

// AIConfig selects the planning/reflection provider (rule-based default).
type AIConfig struct {
	Provider string           `yaml:"provider,omitempty"`
	Ollama   AIProviderOllama `yaml:"ollama,omitempty"`
	Cursor   AIProviderCursor `yaml:"cursor,omitempty"`
}

type AIProviderOllama struct {
	BaseURL string `yaml:"base_url,omitempty"`
	Model   string `yaml:"model,omitempty"`
}

type AIProviderCursor struct {
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
}

type YAMLDateTime struct {
	time.Time
}

func (dt YAMLDateTime) IsZero() bool {
	return dt.Time.IsZero()
}

func (dt YAMLDateTime) MarshalYAML() (any, error) {
	if dt.IsZero() {
		return nil, nil
	}
	return dt.UTC().Format(time.RFC3339), nil
}

func (dt *YAMLDateTime) UnmarshalYAML(unmarshal func(any) error) error {
	var raw string
	if err := unmarshal(&raw); err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("parse updated_at %q: %w", raw, err)
	}
	dt.Time = parsed
	return nil
}

type ValidationError struct {
	Problems []string
}

func (e ValidationError) Error() string {
	return "validation failed: " + strings.Join(e.Problems, "; ")
}

func (f ProjectsFile) Validate() error {
	var problems []string
	seen := map[string]bool{}
	for i, project := range f.Projects {
		path := fmt.Sprintf("projects[%d]", i)
		problems = append(problems, validateID(path+".id", project.ID)...)
		if project.Name == "" {
			problems = append(problems, path+".name is required")
		}
		if seen[project.ID] {
			problems = append(problems, path+".id is duplicated")
		}
		seen[project.ID] = true
		for j, link := range project.Links {
			problems = append(problems, validateLink(fmt.Sprintf("%s.links[%d]", path, j), link)...)
		}
		for j, repo := range project.Repos {
			rp := fmt.Sprintf("%s.repos[%d]", path, j)
			problems = append(problems, validateID(rp+".id", repo.ID)...)
			for hostKey := range repo.PathsByHost {
				if !ValidHostKey(hostKey) {
					problems = append(problems, rp+".paths_by_host has invalid host key "+hostKey)
				}
			}
		}
	}
	return validationResult(problems)
}

func (f ConfigFile) Validate() error {
	var problems []string
	for hostKey := range f.Hosts {
		if !ValidHostKey(hostKey) {
			problems = append(problems, "hosts has invalid key "+hostKey)
		}
	}
	if f.AI.Provider != "" && !validAIProvider(f.AI.Provider) {
		problems = append(problems, "ai.provider is invalid")
	}
	return validationResult(problems)
}

func validAIProvider(p string) bool {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "_", "-")) {
	case "rule-based", "ollama", "cursor":
		return true
	default:
		return false
	}
}

func (f TasksFile) Validate(projects ProjectsFile) error {
	var problems []string
	projectIDs := map[string]bool{}
	for _, project := range projects.Projects {
		projectIDs[project.ID] = true
	}
	seen := map[string]bool{}
	for i, task := range f.Tasks {
		path := fmt.Sprintf("tasks[%d]", i)
		problems = append(problems, validateID(path+".id", task.ID)...)
		problems = append(problems, validateID(path+".project_id", task.ProjectID)...)
		if !projectIDs[task.ProjectID] {
			problems = append(problems, path+".project_id references an unknown project")
		}
		if task.Name == "" {
			problems = append(problems, path+".name is required")
		}
		if !validTaskStatus(task.Status) {
			problems = append(problems, path+".status is invalid")
		}
		if !validPriority(task.Priority) {
			problems = append(problems, path+".priority is invalid")
		}
		if !validDelegationState(task.Delegation.State) {
			problems = append(problems, path+".delegation.state is invalid")
		}
		if seen[task.ID] {
			problems = append(problems, path+".id is duplicated")
		}
		seen[task.ID] = true
		for j, link := range task.Links {
			problems = append(problems, validateLink(fmt.Sprintf("%s.links[%d]", path, j), link)...)
		}
	}
	return validationResult(problems)
}

func (f DirectivesFile) Validate(projects ProjectsFile) error {
	var problems []string
	projectIDs := map[string]bool{}
	for _, project := range projects.Projects {
		projectIDs[project.ID] = true
	}
	seen := map[string]bool{}
	for i, directive := range f.Directives {
		path := fmt.Sprintf("directives[%d]", i)
		problems = append(problems, validateID(path+".id", directive.ID)...)
		if directive.Name == "" {
			problems = append(problems, path+".name is required")
		}
		if directive.Collector == "" {
			problems = append(problems, path+".collector is required")
		}
		if directive.ProjectID != "" && !projectIDs[directive.ProjectID] {
			problems = append(problems, path+".project_id references an unknown project")
		}
		if seen[directive.ID] {
			problems = append(problems, path+".id is duplicated")
		}
		seen[directive.ID] = true
	}
	return validationResult(problems)
}

func validateID(field, id string) []string {
	if id == "" {
		return []string{field + " is required"}
	}
	if !idPattern.MatchString(id) {
		return []string{field + " must match " + idPattern.String()}
	}
	return nil
}

func validateLink(path string, link Link) []string {
	var problems []string
	if link.Type == "" {
		problems = append(problems, path+".type is required")
	}
	if link.URL == "" {
		problems = append(problems, path+".url is required")
	} else if _, err := url.ParseRequestURI(link.URL); err != nil {
		problems = append(problems, path+".url is invalid")
	}
	return problems
}

func validationResult(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return ValidationError{Problems: problems}
}

func validTaskStatus(status TaskStatus) bool {
	switch status {
	case TaskStatusBacklog, TaskStatusReady, TaskStatusInProgress, TaskStatusBlocked, TaskStatusDone, TaskStatusDropped:
		return true
	default:
		return false
	}
}

func validPriority(priority Priority) bool {
	switch priority {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityCritical:
		return true
	default:
		return false
	}
}

func validDelegationState(state DelegationState) bool {
	switch state {
	case DelegationUnknown, DelegationCandidate, DelegationActive, DelegationNotSuitable:
		return true
	default:
		return false
	}
}

func IsValidationError(err error) bool {
	var validationErr ValidationError
	return errors.As(err, &validationErr)
}

type DelegationsFile struct {
	Delegations []AgentWorkEntry `yaml:"delegations"`
}

type AgentWorkEntry struct {
	ID             string         `yaml:"id"`
	TaskID         string         `yaml:"task_id,omitempty"`
	State          AgentWorkState `yaml:"state"`
	Title          string         `yaml:"title"`
	Prompt         string         `yaml:"prompt,omitempty"`
	ExpectedOutput string         `yaml:"expected_output,omitempty"`
	ReviewCriteria string         `yaml:"review_criteria,omitempty"`
	Context        string         `yaml:"context,omitempty"`
	CreatedAt      YAMLDateTime   `yaml:"created_at,omitempty"`
}

type AgentWorkState string

const (
	AgentWorkCandidate   AgentWorkState = "candidate"
	AgentWorkReady       AgentWorkState = "ready"
	AgentWorkActive      AgentWorkState = "active"
	AgentWorkNeedsReview AgentWorkState = "needs_review"
	AgentWorkAccepted    AgentWorkState = "accepted"
	AgentWorkRejected    AgentWorkState = "rejected"
	AgentWorkSuperseded  AgentWorkState = "superseded"
)

func (f DelegationsFile) Validate(tasks TasksFile) error {
	var problems []string
	taskIDs := map[string]bool{}
	for _, t := range tasks.Tasks {
		taskIDs[t.ID] = true
	}
	seen := map[string]bool{}
	for i, d := range f.Delegations {
		path := fmt.Sprintf("delegations[%d]", i)
		problems = append(problems, validateID(path+".id", d.ID)...)
		if d.Title == "" {
			problems = append(problems, path+".title is required")
		}
		if !validAgentWorkState(d.State) {
			problems = append(problems, path+".state is invalid")
		}
		if d.TaskID != "" && !taskIDs[d.TaskID] {
			problems = append(problems, path+".task_id references unknown task")
		}
		if seen[d.ID] {
			problems = append(problems, path+".id is duplicated")
		}
		seen[d.ID] = true
	}
	return validationResult(problems)
}

func validAgentWorkState(s AgentWorkState) bool {
	switch s {
	case AgentWorkCandidate, AgentWorkReady, AgentWorkActive, AgentWorkNeedsReview, AgentWorkAccepted, AgentWorkRejected, AgentWorkSuperseded:
		return true
	default:
		return false
	}
}
