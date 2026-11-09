package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/worktree"
)

// BacklogCommand is one command the backlog collector runs: the backlog CLI
// itself, or a directive's pull_command through the shell.
type BacklogCommand struct {
	Name string
	Args []string
	// Dir is the working directory; Env is added to the inherited environment.
	Dir string
	Env []string
	// DirectiveID routes the command's exec log line to its directive.
	DirectiveID string
}

// BacklogRunner runs a command and returns its stdout. Tests inject one that
// serves canned JSON instead of starting a process.
type BacklogRunner func(ctx context.Context, cmd BacklogCommand, opts *CollectOpts) ([]byte, error)

// BacklogCollector reports each task on a Backlog.md board as a state entity.
// It only reads the board; agents claim and update tasks themselves.
type BacklogCollector struct {
	Clock func() time.Time
	// Run overrides command execution, for tests. Nil runs real processes.
	Run BacklogRunner
}

// backlogTask is the subset of a task the CLI's JSON output carries that the
// collector uses. `task list` omits the plan, description, and path.
type backlogTask struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Status             string   `json:"status"`
	Archived           bool     `json:"archived"`
	Priority           string   `json:"priority"`
	Project            string   `json:"project"`
	Labels             []string `json:"labels"`
	DueDate            string   `json:"dueDate"`
	IsReady            bool     `json:"isReady"`
	Path               string   `json:"path"`
	ImplementationPlan string   `json:"implementationPlan"`
}

// backlogConfig is a directive's parsed config.
type backlogConfig struct {
	directiveID string
	board       string
	command     []string
	pullCommand string
	statuses    map[string]bool
	queueLabel  string
	queueStatus string
	gitEvents   bool
}

// backlogReservedFields are the fields a plan header line may not override.
var backlogReservedFields = map[string]bool{
	"id": true, "status": true, "labels": true, "priority": true, "project": true,
	"due_date": true, "file": true, "ready": true, "queued": true, "plan": true,
	"plan_valid": true, "plan_error": true,
}

// backlogHeaderLine matches a `Key: value` line at the top of a plan.
var backlogHeaderLine = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 _-]*):\s*(.*\S)\s*$`)

func parseBacklogConfig(directive userdata.Directive) (backlogConfig, error) {
	// Resolve the board directory, which is required
	board := expandBacklogHome(strings.TrimSpace(directive.Config["board"]))
	if board == "" {
		return backlogConfig{}, fmt.Errorf("config.board is required for backlog collector")
	}
	cfg := backlogConfig{
		directiveID: directive.ID,
		board:       board,
		command:     strings.Fields(directive.Config["command"]),
		pullCommand: strings.TrimSpace(directive.Config["pull_command"]),
		queueLabel:  strings.TrimSpace(directive.Config["queue_label"]),
		queueStatus: strings.TrimSpace(directive.Config["queue_status"]),
	}
	switch value := strings.TrimSpace(directive.Config["git_events"]); value {
	case "", "false":
	case "true":
		cfg.gitEvents = true
	default:
		return backlogConfig{}, fmt.Errorf("config.git_events must be \"true\" or \"false\"")
	}

	// Fill defaults for the command and queue status
	if len(cfg.command) == 0 {
		cfg.command = []string{"backlog"}
	}
	if cfg.queueStatus == "" {
		cfg.queueStatus = "To Do"
	}

	// Parse the optional status filter, compared case-insensitively
	for _, s := range strings.Split(directive.Config["statuses"], ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			if cfg.statuses == nil {
				cfg.statuses = map[string]bool{}
			}
			cfg.statuses[s] = true
		}
	}
	return cfg, nil
}

// expandBacklogHome resolves a leading ~ and makes the path absolute.
func expandBacklogHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

func (c BacklogCollector) CollectState(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	cfg, err := parseBacklogConfig(directive)
	if err != nil {
		return nil, err
	}

	// Bring the board up to date first, if the user syncs it some way
	if cfg.pullCommand != "" {
		pull := BacklogCommand{Name: "sh", Args: []string{"-c", cfg.pullCommand}}
		if _, err := c.run(ctx, cfg, pull, opts); err != nil {
			return nil, fmt.Errorf("backlog: pull_command failed: %w", err)
		}
	}

	// List every task on the board
	tasks, err := c.listTasks(ctx, cfg, opts)
	if err != nil {
		return nil, err
	}

	// Build one item per task, reading the plan of each queue candidate
	items := make([]StatusItem, 0, len(tasks))
	for _, task := range tasks {
		if task.Archived || !cfg.statusAllowed(task.Status) {
			continue
		}
		if cfg.isCandidate(task) {
			full, err := c.viewTask(ctx, cfg, task.ID, opts)
			if err != nil {
				return nil, err
			}
			task.Path = full.Path
			task.ImplementationPlan = full.ImplementationPlan
		}
		items = append(items, backlogItem(directive.ID, cfg, task))
	}
	return items, nil
}

// statusAllowed applies the directive's optional status filter.
func (cfg backlogConfig) statusAllowed(status string) bool {
	return cfg.statuses == nil || cfg.statuses[strings.ToLower(strings.TrimSpace(status))]
}

// isCandidate reports a task that carries the queue label in the queue status.
// Only these are viewed, so a large board costs one list call plus a few views.
func (cfg backlogConfig) isCandidate(task backlogTask) bool {
	if cfg.queueLabel == "" || !strings.EqualFold(strings.TrimSpace(task.Status), cfg.queueStatus) {
		return false
	}
	for _, l := range task.Labels {
		if strings.EqualFold(strings.TrimSpace(l), cfg.queueLabel) {
			return true
		}
	}
	return false
}

func (c BacklogCollector) listTasks(ctx context.Context, cfg backlogConfig, opts *CollectOpts) ([]backlogTask, error) {
	out, err := c.run(ctx, cfg, cfg.cli("task", "list", "--json"), opts)
	if err != nil {
		return nil, fmt.Errorf("backlog task list: %w", err)
	}
	var resp struct {
		Tasks []backlogTask `json:"tasks"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("backlog task list: unexpected output: %w", err)
	}
	return resp.Tasks, nil
}

func (c BacklogCollector) viewTask(ctx context.Context, cfg backlogConfig, id string, opts *CollectOpts) (backlogTask, error) {
	out, err := c.run(ctx, cfg, cfg.cli("task", "view", id, "--json"), opts)
	if err != nil {
		return backlogTask{}, fmt.Errorf("backlog task view %s: %w", id, err)
	}

	// The CLI wraps the task in {"task": ...}; accept a bare task too
	var wrapped struct {
		Task *backlogTask `json:"task"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		return backlogTask{}, fmt.Errorf("backlog task view %s: unexpected output: %w", id, err)
	}
	if wrapped.Task != nil {
		return *wrapped.Task, nil
	}
	var bare backlogTask
	if err := json.Unmarshal(out, &bare); err != nil {
		return backlogTask{}, fmt.Errorf("backlog task view %s: unexpected output: %w", id, err)
	}
	return bare, nil
}

// cli builds a backlog CLI invocation with the given subcommand arguments.
func (cfg backlogConfig) cli(args ...string) BacklogCommand {
	full := append(append([]string{}, cfg.command[1:]...), args...)
	return BacklogCommand{Name: cfg.command[0], Args: full}
}

// run executes a command against the board, through the injected runner when set.
func (c BacklogCollector) run(ctx context.Context, cfg backlogConfig, cmd BacklogCommand, opts *CollectOpts) ([]byte, error) {
	cmd.Dir = cfg.board
	cmd.DirectiveID = cfg.directiveID
	cmd.Env = append(cmd.Env, "BACKLOG_CWD="+cfg.board)
	if c.Run != nil {
		return c.Run(ctx, cmd, opts)
	}
	return runBacklogCommand(ctx, cmd, opts)
}

// runBacklogCommand runs cmd as a real process with a bounded timeout.
func runBacklogCommand(ctx context.Context, cmd BacklogCommand, opts *CollectOpts) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := append(os.Environ(), cmd.Env...)
	out, err := runAndLogExecContext(cctx, cmd.Name, cmd.Args, cmd.Dir, env, opts, cmd.DirectiveID)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return out, err
	}
	return out, nil
}

// backlogItem maps one task to a state item.
func backlogItem(directiveID string, cfg backlogConfig, task backlogTask) StatusItem {
	// Copy the task's own fields
	fields := map[string]string{
		"id":       task.ID,
		"status":   task.Status,
		"labels":   strings.Join(task.Labels, ","),
		"priority": task.Priority,
		"project":  task.Project,
		"due_date": task.DueDate,
		"ready":    fmt.Sprintf("%t", task.IsReady),
		"queued":   fmt.Sprintf("%t", cfg.isCandidate(task) && task.IsReady),
	}

	// Expose a viewed task's file, plan, and the plan's header lines.
	// The file is "file", not "path": automations read a "path" field as the
	// checkout the work lives in, and the board is not that checkout.
	if task.Path != "" {
		fields["file"] = filepath.Join(cfg.board, task.Path)
	}
	if plan := strings.TrimSpace(task.ImplementationPlan); plan != "" {
		fields["plan"] = plan
		for k, v := range parsePlanHeaders(plan) {
			if !backlogReservedFields[k] {
				fields[k] = v
			}
		}
		checkPlanHeaders(fields)
	}

	// A task listing is state, not activity, so ObservedAt stays unset
	return StatusItem{
		DirectiveID: directiveID,
		Source:      "backlog",
		Kind:        "task",
		Title:       task.Title,
		Summary:     strings.TrimSpace(task.ID + " " + task.Status),
		StableID:    fmt.Sprintf("task:%s:%s", directiveID, task.ID),
		Fields:      fields,
		IsSelf:      true,
	}
}

// checkPlanHeaders records whether a plan's headers can be acted on. A branch
// git would refuse to create is dropped, reported in plan_error, and holds the
// task out of the queue, so the mistake shows on the task rather than as an
// agent run that fails before it starts.
func checkPlanHeaders(fields map[string]string) {
	fields["plan_valid"] = "true"
	branch, ok := fields["branch"]
	if !ok {
		return
	}
	if err := worktree.CheckBranchName(branch); err != nil {
		delete(fields, "branch")
		fields["plan_valid"] = "false"
		fields["plan_error"] = "Branch header: " + err.Error()
		fields["queued"] = "false"
	}
}

// parsePlanHeaders reads the leading `Key: value` lines of a plan, stopping at
// the first line that isn't one. Keys are lower-cased with spaces and dashes
// turned into underscores, so `Repo:` and `Base-Ref:` become repo and base_ref.
func parsePlanHeaders(plan string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(plan, "\n") {
		line = strings.TrimSpace(line)
		if line == "" && len(out) == 0 {
			continue
		}
		m := backlogHeaderLine.FindStringSubmatch(line)
		if m == nil {
			break
		}
		key := strings.ToLower(strings.TrimSpace(m[1]))
		key = strings.NewReplacer(" ", "_", "-", "_").Replace(key)
		out[key] = m[2]
	}
	return out
}

func (c BacklogCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	cfg, err := parseBacklogConfig(directive)
	if err != nil {
		field := "config.board"
		if strings.Contains(err.Error(), "config.git_events") {
			field = "config.git_events"
		}
		return []ValidationIssue{{
			Field:       field,
			Message:     err.Error(),
			Remediation: "set config.board to the board directory and config.git_events to \"true\" or \"false\"",
		}}
	}

	// The board must be a directory on this machine
	var issues []ValidationIssue
	if info, err := os.Stat(cfg.board); err != nil || !info.IsDir() {
		issues = append(issues, ValidationIssue{
			Field:       "config.board",
			Message:     fmt.Sprintf("board %s is not a directory", cfg.board),
			Remediation: "clone or create the board there, or point config.board at it",
		})
	}

	// The backlog CLI must be installed where docent runs
	if _, err := exec.LookPath(cfg.command[0]); err != nil {
		issues = append(issues, ValidationIssue{
			Field:       "config.command",
			Message:     fmt.Sprintf("%q not found on PATH", cfg.command[0]),
			Remediation: "install the Backlog.md CLI (npm i -g backlog.md) or set config.command to its path",
		})
	}
	if cfg.gitEvents {
		h := backlogHistory{cfg: cfg}
		if _, err := h.git(ctx, []string{"rev-parse", "--show-toplevel"}, ""); err != nil {
			issues = append(issues, ValidationIssue{
				Field: "config.git_events", Message: err.Error(),
				Remediation: "install Git and use a board checkout with local commit history, or disable git_events",
			})
		}
	}
	return issues
}
