package automation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/model"
)

// DurableJob is a persisted automation job for the out-of-process worker.
type DurableJob struct {
	ID       string    `json:"id"`
	RuleID   string    `json:"ruleId"`
	Status   JobStatus `json:"status"`
	Action   Action    `json:"action"`
	Context  Context   `json:"context"`
	Error    string    `json:"error,omitempty"`
	Message  string    `json:"message,omitempty"`
	// ConfigDir and JiraDirective let the worker perform post-steps that need
	// credentials (e.g. post.jira_comment). JiraDirective is a serialized
	// userdata.Directive; it is carried as raw JSON so this package does not
	// import userdata (which would create an import cycle). The worker
	// unmarshals it and builds a collector to post the comment.
	ConfigDir     string          `json:"configDir,omitempty"`
	JiraDirective json.RawMessage `json:"jiraDirective,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// QueueDir returns the durable job queue directory.
func QueueDir(stateDir string) string {
	if stateDir == "" {
		stateDir = docentconfig.StateDir()
	}
	return filepath.Join(stateDir, "automation-jobs")
}

// EnqueueAgentJob writes a pending agent job to the durable queue.
func EnqueueAgentJob(stateDir string, job DurableJob) (string, error) {
	dir := QueueDir(stateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if job.ID == "" {
		job.ID = newJobID()
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	job.Status = JobPending
	path := filepath.Join(dir, job.ID+".json")
	b, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return job.ID, nil
}

// ListPendingJobs returns pending durable jobs oldest-first.
func ListPendingJobs(stateDir string) ([]DurableJob, error) {
	dir := QueueDir(stateDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []DurableJob
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var j DurableJob
		if err := json.Unmarshal(b, &j); err != nil {
			continue
		}
		if j.Status == JobPending {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// UpdateDurableJob rewrites a job file with updated fields.
func UpdateDurableJob(stateDir string, job DurableJob) error {
	job.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(QueueDir(stateDir), job.ID+".json"), b, 0o644)
}

// ClaimJob marks a pending job as running. Returns false if already claimed.
func ClaimJob(stateDir, id string) (DurableJob, bool, error) {
	path := filepath.Join(QueueDir(stateDir), id+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return DurableJob{}, false, err
	}
	var j DurableJob
	if err := json.Unmarshal(b, &j); err != nil {
		return DurableJob{}, false, err
	}
	if j.Status != JobPending {
		return j, false, nil
	}
	j.Status = JobRunning
	if err := UpdateDurableJob(stateDir, j); err != nil {
		return j, false, err
	}
	return j, true, nil
}

// ProcessAgentJob runs one durable agent job with the given runner.
func ProcessAgentJob(ctx context.Context, stateDir string, job DurableJob, runner AgentRunner) error {
	ev := EventFromContext(job.RuleID, job.Action, job.Context)
	if err := runner.Run(ctx, job.Action, ev); err != nil {
		job.Status = JobError
		job.Error = err.Error()
		_ = UpdateDurableJob(stateDir, job)
		return err
	}
	job.Status = JobDone
	job.Message = "ok"
	job.Error = ""
	return UpdateDurableJob(stateDir, job)
}

// EventFromContext rebuilds a minimal Event from a stored Context.
func EventFromContext(ruleID string, action Action, ctx Context) Event {
	fields := map[string]string{}
	for k, v := range ctx.Fields {
		fields[k] = v
	}
	sig := &model.Signal{
		Source:     ctx.Source,
		Kind:       ctx.Kind,
		Title:      ctx.Title,
		Summary:    ctx.Summary,
		URL:        ctx.URL,
		Repository: ctx.Repo,
		StableID:   ctx.StableID,
		IsSelf:     ctx.IsSelf,
		Fields:     fields,
		ObservedAt: ctx.FiredAt,
	}
	if ctx.Branch != "" {
		sig.Fields["head_branch"] = ctx.Branch
		sig.Fields["branch"] = ctx.Branch
	}
	if ctx.OpenPath != "" {
		sig.Fields["path"] = ctx.OpenPath
	}
	if ctx.Ticket.Key != "" {
		sig.Fields["key"] = ctx.Ticket.Key
	}
	return Event{
		Rule:      Rule{ID: ruleID, Actions: []Action{action}},
		Trigger:   "signal",
		Signal:    sig,
		TicketKey: ctx.Ticket.Key,
		Match:     ctx.Match,
		From:      ctx.From,
		To:        ctx.To,
		FiredAt:   ctx.FiredAt,
	}
}

// QueuingAgentRunner enqueues agent actions to the durable queue instead of
// running them in-process. The docent-automations worker drains the queue.
type QueuingAgentRunner struct {
	StateDir string
	// ConfigDir and JiraDirectiveJSON are persisted onto each job so the
	// worker can perform post-steps that need credentials (post.jira_comment).
	// JiraDirectiveJSON is a serialized userdata.Directive.
	ConfigDir         string
	JiraDirectiveJSON []byte
}

func (r QueuingAgentRunner) Run(ctx context.Context, action Action, ev Event) error {
	actx := EventContext(ev)
	_, err := EnqueueAgentJob(r.StateDir, DurableJob{
		RuleID:        ev.Rule.ID,
		Action:        action,
		Context:       actx,
		ConfigDir:     r.ConfigDir,
		JiraDirective: json.RawMessage(r.JiraDirectiveJSON),
	})
	return err
}
