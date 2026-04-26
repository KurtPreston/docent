package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

// TaskSignalClassifier is implemented by providers that can classify work signals.
type TaskSignalClassifier interface {
	ClassifyTaskSignals(ctx context.Context, in TaskSignalsInput) (TaskSignalsOutput, error)
}

// TaskSignalsInput is the bounded context for signal triage.
type TaskSignalsInput struct {
	Now         time.Time          `json:"now"`
	Projects    []userdata.Project `json:"projects"`
	Tasks       []userdata.Task    `json:"tasks"`
	OpenSignals []userdata.Signal  `json:"open_signals"`
	DebugDir    string             `json:"-"`
	// StreamOut receives live model output when the provider supports streaming (e.g. Ollama). Typically os.Stderr.
	StreamOut io.Writer `json:"-"`
}

// TaskSignalsOutput is the model output for one classification batch.
type TaskSignalsOutput struct {
	Decisions []TaskSignalDecision `json:"decisions"`
}

// TaskSignalDecision describes what to do with a single open signal.
type TaskSignalDecision struct {
	SignalID   string  `json:"signal_id"`
	Action     string  `json:"action"` // ignore, assign_task, propose_task, pending
	TaskID     string  `json:"task_id,omitempty"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
	// Proposed* used when action is propose_task
	ProposedName        string `json:"proposed_name,omitempty"`
	ProposedProjectID   string `json:"proposed_project_id,omitempty"`
	ProposedDescription string `json:"proposed_description,omitempty"`
	ProposedStatus      string `json:"proposed_status,omitempty"`
	ProposedPriority    string `json:"proposed_priority,omitempty"`
	ProposedNextAction  string `json:"proposed_next_action,omitempty"`
}

// DefaultTaskSignalConfidence is the bar for auto-applying ignore/assign decisions in non-interactive mode.
const DefaultTaskSignalConfidence = 0.85

// BuildTaskSignalsPrompt encodes the instruction and payload.
func BuildTaskSignalsPrompt(instruction string, in TaskSignalsInput) (string, error) {
	payload, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString(instruction)
	buf.WriteString("\n\nReturn only JSON with key: decisions (array of objects with signal_id, action, task_id, confidence, reason, and optional proposed_* fields when proposing a task). Valid actions: ignore, assign_task, propose_task, pending.\n\n")
	buf.Write(payload)
	return buf.String(), nil
}

// ClassifyTaskSignalsRuleBased applies simple deterministic triage.
func ClassifyTaskSignalsRuleBased(_ context.Context, in TaskSignalsInput) (TaskSignalsOutput, error) {
	var out TaskSignalsOutput
	for _, s := range in.OpenSignals {
		lowKind := strings.ToLower(s.Kind)
		switch {
		case lowKind == "branch_wip":
			out.Decisions = append(out.Decisions, TaskSignalDecision{
				SignalID:          s.ID,
				Action:            "propose_task",
				Confidence:        0.55,
				Reason:            "rule: local branch looks like WIP; propose a follow-up task",
				ProposedName:      trimTitle(s.Title, 80),
				ProposedProjectID: s.ProjectID,
				ProposedDescription: s.Summary,
				ProposedStatus:    string(userdata.TaskStatusInProgress),
				ProposedPriority:  string(userdata.PriorityMedium),
				ProposedNextAction: "Decide next steps for this branch or open a PR",
			})
		default:
			out.Decisions = append(out.Decisions, TaskSignalDecision{
				SignalID:   s.ID,
				Action:     "pending",
				Confidence: 0.4,
				Reason:     "rule: no heuristic; needs review or AI",
			})
		}
	}
	return out, nil
}

func trimTitle(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ParseTaskSignalsOutput extracts JSON with decisions array.
func ParseTaskSignalsOutput(raw []byte) (TaskSignalsOutput, error) {
	raw = bytes.TrimSpace(raw)
	start := bytes.IndexByte(raw, '{')
	end := bytes.LastIndexByte(raw, '}')
	if start < 0 || end < start {
		return TaskSignalsOutput{}, fmt.Errorf("output did not contain a JSON object")
	}
	var out TaskSignalsOutput
	if err := json.Unmarshal(raw[start:end+1], &out); err != nil {
		return TaskSignalsOutput{}, err
	}
	return out, nil
}

// SelectTaskSignalClassifier returns a classifier, preferring the provider if it implements it.
func SelectTaskSignalClassifier(p Provider) TaskSignalClassifier {
	if p == nil {
		return nil
	}
	if c, ok := p.(TaskSignalClassifier); ok {
		return c
	}
	return taskSignalRuleAdapter{}
}

type taskSignalRuleAdapter struct{}

func (taskSignalRuleAdapter) ClassifyTaskSignals(ctx context.Context, in TaskSignalsInput) (TaskSignalsOutput, error) {
	return ClassifyTaskSignalsRuleBased(ctx, in)
}
