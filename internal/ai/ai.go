package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

type Provider interface {
	ProposeDayPlan(ctx context.Context, input PlanningInput) (PlanningOutput, error)
	ReflectEndOfDay(ctx context.Context, input PlanningInput) (PlanningOutput, error)
}

type PlanningInput struct {
	Date     time.Time               `json:"date"`
	Projects []userdata.Project      `json:"projects"`
	Tasks    []userdata.Task         `json:"tasks"`
	Statuses []collectors.StatusItem `json:"statuses"`
	Daybook  string                  `json:"daybook,omitempty"`
	Mode     string                  `json:"mode"`
	DebugDir string                  `json:"-"`
	// OllamaStreamOut receives assistant message deltas as they arrive (Ollama provider only).
	OllamaStreamOut io.Writer `json:"-"`
}

type PlanningOutput struct {
	Summary              string                `json:"summary"`
	PrimaryFocus         *FocusBlock           `json:"primary_focus,omitempty"`
	SecondaryFocus       []FocusBlock          `json:"secondary_focus,omitempty"`
	FollowUps            []string              `json:"follow_ups,omitempty"`
	Deferrals            []string              `json:"deferrals,omitempty"`
	NonGoals             []string              `json:"non_goals,omitempty"`
	FocusBlocks          []FocusBlock          `json:"focus_blocks,omitempty"`
	DelegationCandidates []DelegationCandidate `json:"delegation_candidates,omitempty"`
	Questions            []string              `json:"questions,omitempty"`
	ProposedTaskChanges  []ProposedTaskChange  `json:"proposed_task_changes,omitempty"`
}

type FocusBlock struct {
	TaskID string `json:"task_id,omitempty"`
	Title  string `json:"title"`
	Reason string `json:"reason,omitempty"`
}

type DelegationCandidate struct {
	TaskID          string `json:"task_id,omitempty"`
	Title           string `json:"title"`
	Reason          string `json:"reason"`
	SuggestedPrompt string `json:"suggested_prompt"`
}

type ProposedTaskChange struct {
	TaskID string `json:"task_id"`
	Field  string `json:"field"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

type RuleBasedProvider struct{}

func (RuleBasedProvider) ProposeDayPlan(_ context.Context, input PlanningInput) (PlanningOutput, error) {
	output := PlanningOutput{Summary: summarize(input)}
	for _, task := range input.Tasks {
		if task.Status == userdata.TaskStatusDone || task.Status == userdata.TaskStatusDropped {
			continue
		}
		if task.Priority == userdata.PriorityCritical || task.Priority == userdata.PriorityHigh || len(output.FocusBlocks) < 3 {
			output.FocusBlocks = append(output.FocusBlocks, FocusBlock{
				TaskID: task.ID,
				Title:  task.Name,
				Reason: fmt.Sprintf("priority=%s status=%s", task.Priority, task.Status),
			})
		}
		if task.Delegation.State == userdata.DelegationCandidate || strings.Contains(strings.ToLower(task.NextAction), "agent") {
			output.DelegationCandidates = append(output.DelegationCandidates, DelegationCandidate{
				TaskID:          task.ID,
				Title:           task.Name,
				Reason:          nonEmpty(task.Delegation.Reason, "Task is marked as a delegation candidate."),
				SuggestedPrompt: nonEmpty(task.Delegation.SuggestedPrompt, "Investigate the next action and report back with recommended changes."),
			})
		}
	}
	if len(output.FocusBlocks) == 0 {
		output.FocusBlocks = append(output.FocusBlocks, FocusBlock{Title: "Review collected status", Reason: "No active tasks are configured yet."})
	}
	if len(output.FocusBlocks) > 0 {
		p := output.FocusBlocks[0]
		output.PrimaryFocus = &FocusBlock{TaskID: p.TaskID, Title: p.Title, Reason: p.Reason}
	}
	if len(output.FocusBlocks) > 1 {
		for i := 1; i < len(output.FocusBlocks) && i < 3; i++ {
			b := output.FocusBlocks[i]
			output.SecondaryFocus = append(output.SecondaryFocus, FocusBlock{TaskID: b.TaskID, Title: b.Title, Reason: b.Reason})
		}
	}
	for _, s := range input.Statuses {
		if s.AttentionClass == "urgent" || s.AttentionClass == "waiting_on_me" {
			line := s.Title
			if s.URL != "" {
				line += " (" + s.URL + ")"
			}
			output.FollowUps = append(output.FollowUps, line)
		}
		if s.AttentionClass == "deferrable" || s.AttentionClass == "informational" {
			if strings.Contains(strings.ToLower(s.Summary), "clean") && s.Source == "local-git" {
				output.NonGoals = append(output.NonGoals, "Polish clean repo: "+s.Title)
			}
		}
	}
	output.Questions = []string{"Did priorities change based on the latest status?", "Is anything blocked that should be delegated or dropped?"}
	return output, nil
}

func (RuleBasedProvider) ReflectEndOfDay(_ context.Context, input PlanningInput) (PlanningOutput, error) {
	output := PlanningOutput{Summary: summarize(input)}
	output.Questions = []string{
		"What did not go according to plan?",
		"How did the day feel?",
		"What should be attenuated or emphasized tomorrow?",
		"Did any task priority or status change?",
	}
	return output, nil
}

type CursorCLIProvider struct {
	Command string
	Args    []string
}

func (p CursorCLIProvider) ProposeDayPlan(ctx context.Context, input PlanningInput) (PlanningOutput, error) {
	return p.run(ctx, "Create a concise daily plan as JSON matching the requested schema.", input)
}

func (p CursorCLIProvider) ReflectEndOfDay(ctx context.Context, input PlanningInput) (PlanningOutput, error) {
	return p.run(ctx, "Create a concise end-of-day reflection as JSON matching the requested schema.", input)
}

func (p CursorCLIProvider) run(ctx context.Context, instruction string, input PlanningInput) (PlanningOutput, error) {
	command := p.Command
	if command == "" {
		command = "cursor-agent"
	}
	payload, err := BuildPrompt(instruction, input)
	if err != nil {
		return PlanningOutput{}, err
	}
	args := p.Args
	if len(args) == 0 {
		args = []string{"-p", payload}
	}
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.Output()
	if err != nil {
		return PlanningOutput{}, err
	}
	return ParsePlanningOutput(output)
}

func BuildPrompt(instruction string, input PlanningInput) (string, error) {
	payload, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString(instruction)
	buf.WriteString("\n\nReturn only JSON with keys: summary, primary_focus, secondary_focus, follow_ups, deferrals, non_goals, focus_blocks, delegation_candidates, questions, proposed_task_changes.\n")
	buf.WriteString("Never include credentials, secrets, or unrelated local data.\n\n")
	buf.Write(payload)
	return buf.String(), nil
}

// NormalizePlanningOutput fills primary/secondary structured fields from legacy focus_blocks when missing.
func NormalizePlanningOutput(out *PlanningOutput) {
	if out == nil {
		return
	}
	if out.PrimaryFocus == nil && len(out.FocusBlocks) > 0 {
		b := out.FocusBlocks[0]
		out.PrimaryFocus = &FocusBlock{TaskID: b.TaskID, Title: b.Title, Reason: b.Reason}
	}
	if len(out.SecondaryFocus) == 0 && len(out.FocusBlocks) > 1 {
		for i := 1; i < len(out.FocusBlocks) && len(out.SecondaryFocus) < 2; i++ {
			b := out.FocusBlocks[i]
			out.SecondaryFocus = append(out.SecondaryFocus, FocusBlock{TaskID: b.TaskID, Title: b.Title, Reason: b.Reason})
		}
	}
}

func ParsePlanningOutput(raw []byte) (PlanningOutput, error) {
	raw = bytes.TrimSpace(raw)
	start := bytes.IndexByte(raw, '{')
	end := bytes.LastIndexByte(raw, '}')
	if start < 0 || end < start {
		return PlanningOutput{}, fmt.Errorf("AI output did not contain a JSON object")
	}
	var wire struct {
		Summary              string            `json:"summary"`
		PrimaryFocus         json.RawMessage   `json:"primary_focus,omitempty"`
		SecondaryFocus       json.RawMessage   `json:"secondary_focus,omitempty"`
		FollowUps            []string          `json:"follow_ups,omitempty"`
		Deferrals            []string          `json:"deferrals,omitempty"`
		NonGoals             []string          `json:"non_goals,omitempty"`
		FocusBlocks          []json.RawMessage `json:"focus_blocks,omitempty"`
		DelegationCandidates []json.RawMessage `json:"delegation_candidates,omitempty"`
		Questions            []string          `json:"questions,omitempty"`
		ProposedTaskChanges  []json.RawMessage `json:"proposed_task_changes,omitempty"`
	}
	if err := json.Unmarshal(raw[start:end+1], &wire); err != nil {
		return PlanningOutput{}, err
	}
	primary, err := parseOptionalFocusBlock(wire.PrimaryFocus)
	if err != nil {
		return PlanningOutput{}, fmt.Errorf("parse primary_focus: %w", err)
	}
	secondary, err := parseFocusBlockList(wire.SecondaryFocus)
	if err != nil {
		return PlanningOutput{}, fmt.Errorf("parse secondary_focus: %w", err)
	}
	focusBlocks, err := parseFocusBlockRawList(wire.FocusBlocks)
	if err != nil {
		return PlanningOutput{}, fmt.Errorf("parse focus_blocks: %w", err)
	}
	delegationCandidates, err := parseDelegationCandidateRawList(wire.DelegationCandidates)
	if err != nil {
		return PlanningOutput{}, fmt.Errorf("parse delegation_candidates: %w", err)
	}
	proposedTaskChanges, err := parseProposedTaskChangeRawList(wire.ProposedTaskChanges)
	if err != nil {
		return PlanningOutput{}, fmt.Errorf("parse proposed_task_changes: %w", err)
	}
	output := PlanningOutput{
		Summary:              wire.Summary,
		PrimaryFocus:         primary,
		SecondaryFocus:       secondary,
		FollowUps:            wire.FollowUps,
		Deferrals:            wire.Deferrals,
		NonGoals:             wire.NonGoals,
		FocusBlocks:          focusBlocks,
		DelegationCandidates: delegationCandidates,
		Questions:            wire.Questions,
		ProposedTaskChanges:  proposedTaskChanges,
	}
	if output.Summary == "" {
		return PlanningOutput{}, fmt.Errorf("AI output summary is required")
	}
	return output, nil
}

func parseOptionalFocusBlock(raw json.RawMessage) (*FocusBlock, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var title string
		if err := json.Unmarshal(trimmed, &title); err != nil {
			return nil, err
		}
		title = strings.TrimSpace(title)
		if title == "" {
			return nil, nil
		}
		return &FocusBlock{Title: title}, nil
	}
	var block FocusBlock
	if err := json.Unmarshal(trimmed, &block); err != nil {
		return nil, err
	}
	if strings.TrimSpace(block.Title) == "" {
		return nil, nil
	}
	return &block, nil
}

func parseFocusBlockList(raw json.RawMessage) ([]FocusBlock, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var asArray []json.RawMessage
	if err := json.Unmarshal(trimmed, &asArray); err == nil {
		out := make([]FocusBlock, 0, len(asArray))
		for _, item := range asArray {
			block, err := parseOptionalFocusBlock(item)
			if err != nil {
				return nil, err
			}
			if block != nil {
				out = append(out, *block)
			}
		}
		return out, nil
	}
	block, err := parseOptionalFocusBlock(trimmed)
	if err != nil {
		return nil, err
	}
	if block == nil {
		return nil, nil
	}
	return []FocusBlock{*block}, nil
}

func parseFocusBlockRawList(rawItems []json.RawMessage) ([]FocusBlock, error) {
	if len(rawItems) == 0 {
		return nil, nil
	}
	out := make([]FocusBlock, 0, len(rawItems))
	for _, item := range rawItems {
		block, err := parseOptionalFocusBlock(item)
		if err != nil {
			return nil, err
		}
		if block != nil {
			out = append(out, *block)
		}
	}
	return out, nil
}

func parseOptionalDelegationCandidate(raw json.RawMessage) (*DelegationCandidate, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var title string
		if err := json.Unmarshal(trimmed, &title); err != nil {
			return nil, err
		}
		title = strings.TrimSpace(title)
		if title == "" {
			return nil, nil
		}
		return &DelegationCandidate{Title: title}, nil
	}
	var dc DelegationCandidate
	if err := json.Unmarshal(trimmed, &dc); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dc.Title) == "" {
		return nil, nil
	}
	return &dc, nil
}

func parseDelegationCandidateRawList(rawItems []json.RawMessage) ([]DelegationCandidate, error) {
	if len(rawItems) == 0 {
		return nil, nil
	}
	out := make([]DelegationCandidate, 0, len(rawItems))
	for _, item := range rawItems {
		dc, err := parseOptionalDelegationCandidate(item)
		if err != nil {
			return nil, err
		}
		if dc != nil {
			out = append(out, *dc)
		}
	}
	return out, nil
}

// proposedTaskChangeWire matches common model variants (schema + alternate keys).
type proposedTaskChangeWire struct {
	TaskID    string `json:"task_id"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	Reason    string `json:"reason"`
	Change    string `json:"change"`
	Rationale string `json:"rationale"`
}

func parseOptionalProposedTaskChange(raw json.RawMessage) (*ProposedTaskChange, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		return &ProposedTaskChange{Value: s}, nil
	}
	var w proposedTaskChangeWire
	if err := json.Unmarshal(trimmed, &w); err != nil {
		return nil, err
	}
	value := w.Value
	if value == "" {
		value = w.Change
	}
	reason := w.Reason
	if reason == "" {
		reason = w.Rationale
	}
	if w.TaskID == "" && w.Field == "" && value == "" && reason == "" {
		return nil, nil
	}
	return &ProposedTaskChange{
		TaskID: w.TaskID,
		Field:  w.Field,
		Value:  value,
		Reason: reason,
	}, nil
}

func parseProposedTaskChangeRawList(rawItems []json.RawMessage) ([]ProposedTaskChange, error) {
	if len(rawItems) == 0 {
		return nil, nil
	}
	out := make([]ProposedTaskChange, 0, len(rawItems))
	for _, item := range rawItems {
		ptc, err := parseOptionalProposedTaskChange(item)
		if err != nil {
			return nil, err
		}
		if ptc != nil {
			out = append(out, *ptc)
		}
	}
	return out, nil
}

func summarize(input PlanningInput) string {
	return fmt.Sprintf("Reviewed %d project(s), %d task(s), and %d status item(s).", len(input.Projects), len(input.Tasks), len(input.Statuses))
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
