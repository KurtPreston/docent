package server

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/KurtPreston/docent/libs/report"
)

// Report generation can take minutes (LLM providers), so docentd runs it in a
// background goroutine and the dashboard polls / streams for the result. Jobs
// are ephemeral and in-memory: bounded by count and pruned by age, and lost on
// restart. That's fine — a report is cheap to re-run and the Markdown is
// downloaded client-side.
const (
	reportJobTTL = 30 * time.Minute
	reportJobCap = 64
)

type reportStatus string

const (
	reportPending reportStatus = "pending"
	reportRunning reportStatus = "running"
	reportDone    reportStatus = "done"
	reportError   reportStatus = "error"
)

// reportRunMeta echoes the resolved run so the UI can show what actually ran
// (mode default lookback/scope may differ from the blank form fields).
type reportRunMeta struct {
	Mode         string `json:"mode"`
	ModeName     string `json:"modeName"`
	Scope        string `json:"scope"`
	LookbackDays int    `json:"lookbackDays"`
	Statuses     int    `json:"statuses"`
}

// reportCollectorView is the JSON shape for a single collector progress row.
type reportCollectorView struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Completed   int    `json:"completed,omitempty"`
	Total       int    `json:"total,omitempty"`
}

// reportEvent is one SSE frame (and one entry in the job's replay buffer).
// Type is one of: phase | collector | token | thinking | done | error.
type reportEvent struct {
	Type      string               `json:"type"`
	Phase     string               `json:"phase,omitempty"`
	Collector *reportCollectorView `json:"collector,omitempty"`
	Text      string               `json:"text,omitempty"`
	Markdown  string               `json:"markdown,omitempty"`
	Meta      *reportRunMeta       `json:"meta,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type reportJob struct {
	id        string
	status    reportStatus
	markdown  string
	meta      *reportRunMeta
	errMsg    string
	createdAt time.Time
	updatedAt time.Time

	// Live progress for SSE subscribers.
	events  []reportEvent
	partial strings.Builder
	subs    map[chan reportEvent]struct{}
	// terminal is set once finish/fail has been called so late
	// subscribers only get the replay (no hanging wait for live events).
	terminal bool
}

// reportJobView is the JSON snapshot returned to pollers. It's a value copy so
// it can be marshaled outside the store lock.
type reportJobView struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Markdown string         `json:"markdown,omitempty"`
	Meta     *reportRunMeta `json:"meta,omitempty"`
	Error    string         `json:"error,omitempty"`
	Phase    string         `json:"phase,omitempty"`
	Partial  string         `json:"partial,omitempty"`
}

func (j *reportJob) view() reportJobView {
	phase := ""
	for i := len(j.events) - 1; i >= 0; i-- {
		if j.events[i].Type == "phase" {
			phase = j.events[i].Phase
			break
		}
	}
	return reportJobView{
		ID:       j.id,
		Status:   string(j.status),
		Markdown: j.markdown,
		Meta:     j.meta,
		Error:    j.errMsg,
		Phase:    phase,
		Partial:  j.partial.String(),
	}
}

// reportStore is a small mutex-guarded map of in-flight and recently-finished
// report jobs.
type reportStore struct {
	mu   sync.Mutex
	jobs map[string]*reportJob
}

func newReportStore() *reportStore {
	return &reportStore{jobs: make(map[string]*reportJob)}
}

// start registers a new pending job and returns its id, pruning stale/excess
// jobs first.
func (st *reportStore) start() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked()
	id := newReportID()
	now := time.Now()
	st.jobs[id] = &reportJob{
		id:        id,
		status:    reportPending,
		createdAt: now,
		updatedAt: now,
		subs:      make(map[chan reportEvent]struct{}),
	}
	return id
}

func (st *reportStore) markRunning(id string) {
	st.update(id, func(j *reportJob) {
		if j.status == reportPending {
			j.status = reportRunning
		}
	})
}

func (st *reportStore) finish(id string, res report.Result) {
	meta := &reportRunMeta{
		Mode:         res.Run.ModeID,
		ModeName:     res.Run.ModeName,
		Scope:        string(res.Run.Scope),
		LookbackDays: res.Run.LookbackDays,
		Statuses:     res.Statuses,
	}
	st.emit(id, reportEvent{
		Type:     "done",
		Markdown: res.Markdown,
		Meta:     meta,
	})
	st.update(id, func(j *reportJob) {
		j.status = reportDone
		j.markdown = res.Markdown
		j.meta = meta
		j.terminal = true
		st.closeSubsLocked(j)
	})
}

func (st *reportStore) fail(id string, err error) {
	msg := err.Error()
	st.emit(id, reportEvent{Type: "error", Error: msg})
	st.update(id, func(j *reportJob) {
		j.status = reportError
		j.errMsg = msg
		j.terminal = true
		st.closeSubsLocked(j)
	})
}

// emit appends an event to the job's replay buffer, updates partial markdown
// for token events, and fans the event out to all live subscribers. Safe to
// call from collector/AI callbacks (may run on arbitrary goroutines).
func (st *reportStore) emit(id string, ev reportEvent) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j, ok := st.jobs[id]
	if !ok || j.terminal {
		return
	}
	j.events = append(j.events, ev)
	if ev.Type == "token" && ev.Text != "" {
		j.partial.WriteString(ev.Text)
	}
	j.updatedAt = time.Now()
	terminal := ev.Type == "done" || ev.Type == "error"
	for ch := range j.subs {
		if terminal {
			// Terminal events must not be dropped: make room if the buffer
			// is full, then send (or leave the subscriber to synthesize from
			// the job snapshot when the channel closes).
			select {
			case ch <- ev:
			default:
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- ev:
				default:
				}
			}
			continue
		}
		select {
		case ch <- ev:
		default:
			// Slow subscriber: drop rather than block generation.
		}
	}
}

// subscribe returns a snapshot of events so far plus a channel of future
// events. cancel removes the subscription. If the job is already terminal,
// ch is nil and only the replay is returned (caller should not wait).
func (st *reportStore) subscribe(id string) (replay []reportEvent, ch chan reportEvent, cancel func(), ok bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j, ok := st.jobs[id]
	if !ok {
		return nil, nil, nil, false
	}
	replay = make([]reportEvent, len(j.events))
	copy(replay, j.events)
	if j.terminal {
		return replay, nil, func() {}, true
	}
	ch = make(chan reportEvent, 64)
	j.subs[ch] = struct{}{}
	cancel = func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		if job, still := st.jobs[id]; still {
			if _, present := job.subs[ch]; present {
				delete(job.subs, ch)
				close(ch)
			}
		}
	}
	return replay, ch, cancel, true
}

func (st *reportStore) closeSubsLocked(j *reportJob) {
	for ch := range j.subs {
		close(ch)
		delete(j.subs, ch)
	}
}

func (st *reportStore) update(id string, fn func(*reportJob)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if j, ok := st.jobs[id]; ok {
		fn(j)
		j.updatedAt = time.Now()
	}
}

func (st *reportStore) get(id string) (reportJobView, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j, ok := st.jobs[id]
	if !ok {
		return reportJobView{}, false
	}
	return j.view(), true
}

// pruneLocked drops jobs older than the TTL and, if still over the cap,
// evicts the least-recently-updated jobs. Callers must hold st.mu.
func (st *reportStore) pruneLocked() {
	cutoff := time.Now().Add(-reportJobTTL)
	for id, j := range st.jobs {
		if j.updatedAt.Before(cutoff) {
			st.closeSubsLocked(j)
			delete(st.jobs, id)
		}
	}
	for len(st.jobs) >= reportJobCap {
		var oldestID string
		var oldest time.Time
		for id, j := range st.jobs {
			if oldestID == "" || j.updatedAt.Before(oldest) {
				oldestID, oldest = id, j.updatedAt
			}
		}
		if oldestID == "" {
			break
		}
		st.closeSubsLocked(st.jobs[oldestID])
		delete(st.jobs, oldestID)
	}
}

func newReportID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; a timestamp-based id keeps things moving.
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))
	}
	return hex.EncodeToString(b[:])
}
