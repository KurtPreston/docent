package taskupdate

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"github.com/kurt/slakkr-ai/internal/workflow"
)

// Options controls update_tasks runs.
type Options struct {
	DryRun        bool
	MinConfidence float64
	Now           time.Time
	// BeforeClassify runs after collectors finish and before any AI call (e.g. close the directive progress UI).
	BeforeClassify func()
	// OllamaStreamOut receives streamed tokens when using the Ollama provider; nil means no streaming sink.
	OllamaStreamOut io.Writer
}

// Result summarizes a run.
type Result struct {
	Scanned     int
	OpenForAI   int
	AutoAssign  int
	AutoIgnore  int
	ProposedNew int
	Pending     int
	DryRun      bool
}

// Run loads directives, collects status, merges signals, classifies, and optionally persists.
func Run(ctx context.Context, d workflow.Deps, userdataDir string, opts Options) (Result, error) {
	if opts.MinConfidence <= 0 {
		opts.MinConfidence = ai.DefaultTaskSignalConfidence
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	store := userdata.NewStore(userdataDir)
	projects, err := store.LoadProjects()
	if err != nil {
		return Result{}, err
	}
	tasks, err := store.LoadTasks(projects)
	if err != nil {
		return Result{}, err
	}
	signalsFile, err := store.LoadSignals(projects, tasks)
	if err != nil {
		return Result{}, err
	}
	proposedFile, err := store.LoadProposedTasks(projects, tasks)
	if err != nil {
		return Result{}, err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return Result{}, err
	}
	planInput, _, err := workflow.BuildPlanningInput(ctx, d, userdataDir, now, "update_tasks")
	if err != nil {
		return Result{}, err
	}
	byID := mergeScannedSignals(planInput.Statuses, signalsFile)
	res := Result{DryRun: opts.DryRun, Scanned: len(byID)}

	// Pass 1: deterministic task assignment
	for id, s := range byID {
		if s.Resolution != "" && s.Resolution != userdata.SignalResolutionPending {
			continue
		}
		n := FromUserdataSignal(s)
		if taskID := DeterministicTaskMatch(n, tasks.Tasks); taskID != "" {
			s.Resolution = userdata.SignalResolutionTask
			s.TaskID = taskID
			s.Reason = "matched existing task (link or issue key)"
			s.ClassifiedAt = userdata.YAMLDateTime{Time: now}
			byID[id] = s
			res.AutoAssign++
		}
	}

	// Open signals: still need AI (or are fully pending)
	var open []userdata.Signal
	for _, s := range byID {
		if s.Resolution == "" || s.Resolution == userdata.SignalResolutionPending {
			open = append(open, s)
		}
	}
	res.OpenForAI = len(open)
	if len(open) == 0 {
		if err := persist(store, projects, tasks, byID, proposedFile, opts); err != nil {
			return res, err
		}
		return res, nil
	}
	if opts.BeforeClassify != nil {
		opts.BeforeClassify()
	}
	classifier := ai.SelectTaskSignalClassifier(ai.SelectProvider(cfg.AI, nil))
	aiIn := ai.TaskSignalsInput{
		Now:         now,
		Projects:    projects.Projects,
		Tasks:       tasks.Tasks,
		OpenSignals: open,
		DebugDir:    filepath.Join(userdataDir, "status-cache", "ai-debug"),
		StreamOut:   opts.OllamaStreamOut,
	}
	aiOut, err := classifier.ClassifyTaskSignals(ctx, aiIn)
	if err != nil {
		return res, err
	}
	decByID := map[string]ai.TaskSignalDecision{}
	for _, dec := range aiOut.Decisions {
		decByID[dec.SignalID] = dec
	}

	existingProp := map[string]bool{}
	for _, p := range proposedFile.Proposed {
		for _, sid := range p.SourceSignalIDs {
			existingProp[sid] = true
		}
	}
	for id, s := range byID {
		if s.Resolution != "" && s.Resolution != userdata.SignalResolutionPending {
			continue
		}
		dec, ok := decByID[id]
		if !ok {
			res.Pending++
			continue
		}
		switch dec.Action {
		case "ignore":
			if dec.Confidence < opts.MinConfidence {
				res.Pending++
				continue
			}
			s.Resolution = userdata.SignalResolutionIgnored
			s.Reason = dec.Reason
			s.ClassifiedAt = userdata.YAMLDateTime{Time: now}
			byID[id] = s
			res.AutoIgnore++
		case "assign_task":
			if dec.Confidence < opts.MinConfidence {
				res.Pending++
				continue
			}
			if dec.TaskID == "" {
				res.Pending++
				continue
			}
			if !taskExists(tasks, dec.TaskID) {
				res.Pending++
				continue
			}
			s.Resolution = userdata.SignalResolutionTask
			s.TaskID = dec.TaskID
			s.Reason = dec.Reason
			s.ClassifiedAt = userdata.YAMLDateTime{Time: now}
			byID[id] = s
			res.AutoAssign++
		case "propose_task":
			if dec.Confidence < 0.5 {
				res.Pending++
				continue
			}
			if existingProp[id] {
				s.Reason = "already proposed; awaiting confirmation"
				byID[id] = s
				res.Pending++
				break
			}
			proj := dec.ProposedProjectID
			if proj == "" {
				proj = s.ProjectID
			}
			if proj == "" {
				res.Pending++
				continue
			}
			cf := dec.Confidence
			pt := userdata.ProposedTask{
				ID:              proposedTaskID(now),
				SourceSignalIDs: []string{id},
				ProjectID:       proj,
				Name:            firstNonEmpty(dec.ProposedName, s.Title, "Triage: "+s.Source),
				Description:     firstNonEmpty(dec.ProposedDescription, s.Summary),
				Status:          taskStatusOr(dec.ProposedStatus, userdata.TaskStatusReady),
				Priority:        priorityOr(dec.ProposedPriority, userdata.PriorityMedium),
				NextAction:      firstNonEmpty(dec.ProposedNextAction, "Review and confirm or dismiss this proposed task"),
				Confidence:      &cf,
				Reason:          dec.Reason,
				CreatedAt:       userdata.YAMLDateTime{Time: now},
			}
			proposedFile.Proposed = append(proposedFile.Proposed, pt)
			existingProp[id] = true
			s.Reason = "proposed as new task; awaiting user confirmation"
			byID[id] = s
			res.ProposedNew++
		default:
			res.Pending++
		}
	}

	if err := persist(store, projects, tasks, byID, proposedFile, opts); err != nil {
		return res, err
	}
	return res, nil
}

func taskExists(t userdata.TasksFile, id string) bool {
	for _, x := range t.Tasks {
		if x.ID == id {
			return true
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func taskStatusOr(s string, def userdata.TaskStatus) userdata.TaskStatus {
	if s == "" {
		return def
	}
	return userdata.TaskStatus(s)
}

func priorityOr(s string, def userdata.Priority) userdata.Priority {
	if s == "" {
		return def
	}
	return userdata.Priority(s)
}

func proposedTaskID(now time.Time) string {
	// e.g. pt-p17140344001234 — matches ^pt-[a-z][a-z0-9-]*$
	return fmt.Sprintf("pt-p%013d%04d", now.Unix(), (now.Nanosecond()/1_000_000)%10_000)
}

func mergeScannedSignals(statuses []collectors.StatusItem, file userdata.SignalsFile) map[string]userdata.Signal {
	byID := map[string]userdata.Signal{}
	// start from file
	for _, s := range file.Signals {
		byID[s.ID] = s
	}
	for _, st := range statuses {
		if !IsActionableStatus(st) {
			continue
		}
		n := FromStatusItem(st)
		snapshot := userdata.SignalsFile{Signals: listSignals(byID)}
		merged, _ := MergeWithExisting(n, snapshot)
		byID[merged.ID] = merged
	}
	return byID
}

func listSignals(m map[string]userdata.Signal) []userdata.Signal {
	out := make([]userdata.Signal, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	return out
}

func persist(store userdata.Store, projects userdata.ProjectsFile, tasks userdata.TasksFile, byID map[string]userdata.Signal, proposed userdata.ProposedTasksFile, opts Options) error {
	if opts.DryRun {
		return nil
	}
	var signals []userdata.Signal
	for _, s := range byID {
		signals = append(signals, s)
	}
	out := userdata.SignalsFile{Signals: signals}
	if err := store.SaveSignals(projects, tasks, out); err != nil {
		return err
	}
	return store.SaveProposedTasks(projects, tasks, proposed)
}
