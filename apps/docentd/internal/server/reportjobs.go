package server

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/KurtPreston/docent/libs/report"
)

// Report generation can take minutes (LLM providers), so docentd runs it in a
// background goroutine and the dashboard polls for the result. Jobs are
// ephemeral and in-memory: bounded by count and pruned by age, and lost on
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

type reportJob struct {
	id        string
	status    reportStatus
	markdown  string
	meta      *reportRunMeta
	errMsg    string
	createdAt time.Time
	updatedAt time.Time
}

// reportJobView is the JSON snapshot returned to pollers. It's a value copy so
// it can be marshaled outside the store lock.
type reportJobView struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Markdown string         `json:"markdown,omitempty"`
	Meta     *reportRunMeta `json:"meta,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func (j *reportJob) view() reportJobView {
	return reportJobView{
		ID:       j.id,
		Status:   string(j.status),
		Markdown: j.markdown,
		Meta:     j.meta,
		Error:    j.errMsg,
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
	st.jobs[id] = &reportJob{id: id, status: reportPending, createdAt: now, updatedAt: now}
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
	st.update(id, func(j *reportJob) {
		j.status = reportDone
		j.markdown = res.Markdown
		j.meta = &reportRunMeta{
			Mode:         res.Run.ModeID,
			ModeName:     res.Run.ModeName,
			Scope:        string(res.Run.Scope),
			LookbackDays: res.Run.LookbackDays,
			Statuses:     res.Statuses,
		}
	})
}

func (st *reportStore) fail(id string, err error) {
	st.update(id, func(j *reportJob) {
		j.status = reportError
		j.errMsg = err.Error()
	})
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
