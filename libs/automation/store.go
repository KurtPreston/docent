package automation

import (
	"sync"
	"time"
)

// JobStatus is the lifecycle state of a dispatched automation job.
type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobError   JobStatus = "error"
	JobSkipped JobStatus = "skipped"
)

// Job is one fired automation (rule + event) tracked for history / cooldown.
type Job struct {
	ID        string    `json:"id"`
	RuleID    string    `json:"ruleId"`
	DedupeKey string    `json:"dedupeKey"`
	Status    JobStatus `json:"status"`
	Message   string    `json:"message,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store tracks recent jobs for cooldown/idempotency and history.
type Store struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	byDedupe map[string]time.Time // dedupeKey → last successful/running fire
	cap      int
	ttl      time.Duration
}

// NewStore returns an in-memory job store.
func NewStore() *Store {
	return &Store{
		jobs:     map[string]*Job{},
		byDedupe: map[string]time.Time{},
		cap:      256,
		ttl:      24 * time.Hour,
	}
}

// ShouldSkip reports whether a dedupe key is still within its cooldown window.
func (s *Store) ShouldSkip(dedupeKey, cooldown string, now time.Time) bool {
	if dedupeKey == "" {
		return false
	}
	cd, err := ParseDuration(cooldown)
	if err != nil || cd <= 0 {
		// No cooldown configured: still skip if an identical job is currently running.
		s.mu.Lock()
		defer s.mu.Unlock()
		if t, ok := s.byDedupe[dedupeKey]; ok && now.Sub(t) < 5*time.Second {
			return true
		}
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byDedupe[dedupeKey]
	if !ok {
		return false
	}
	return now.Sub(t) < cd
}

// Start records a new pending→running job and stamps the dedupe key.
func (s *Store) Start(id, ruleID, dedupeKey string, now time.Time) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	j := &Job{
		ID:        id,
		RuleID:    ruleID,
		DedupeKey: dedupeKey,
		Status:    JobRunning,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.jobs[id] = j
	if dedupeKey != "" {
		s.byDedupe[dedupeKey] = now
	}
	return cloneJob(j)
}

// Finish marks a job done.
func (s *Store) Finish(id, message string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return
	}
	j.Status = JobDone
	j.Message = message
	j.UpdatedAt = now
}

// Fail marks a job errored. The dedupe stamp remains so cooldown still applies
// (avoids tight retry loops on persistent failures).
func (s *Store) Fail(id, errMsg string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return
	}
	j.Status = JobError
	j.Error = errMsg
	j.UpdatedAt = now
}

// Skip records a skipped fire (cooldown) for history.
func (s *Store) Skip(id, ruleID, dedupeKey, reason string, now time.Time) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	j := &Job{
		ID:        id,
		RuleID:    ruleID,
		DedupeKey: dedupeKey,
		Status:    JobSkipped,
		Message:   reason,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.jobs[id] = j
	return cloneJob(j)
}

// List returns jobs newest-first, up to limit (0 = all).
func (s *Store) List(limit int) []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, *j)
	}
	// newest first
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].CreatedAt.After(out[i].CreatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Get returns one job by id.
func (s *Store) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *cloneJob(j), true
}

func (s *Store) pruneLocked(now time.Time) {
	for id, j := range s.jobs {
		if now.Sub(j.UpdatedAt) > s.ttl {
			delete(s.jobs, id)
		}
	}
	for k, t := range s.byDedupe {
		if now.Sub(t) > s.ttl {
			delete(s.byDedupe, k)
		}
	}
	if s.cap > 0 && len(s.jobs) > s.cap {
		// Drop oldest until under cap.
		type pair struct {
			id string
			t  time.Time
		}
		all := make([]pair, 0, len(s.jobs))
		for id, j := range s.jobs {
			all = append(all, pair{id, j.UpdatedAt})
		}
		for i := 0; i < len(all); i++ {
			for j := i + 1; j < len(all); j++ {
				if all[j].t.Before(all[i].t) {
					all[i], all[j] = all[j], all[i]
				}
			}
		}
		for i := 0; i < len(all)-s.cap; i++ {
			delete(s.jobs, all[i].id)
		}
	}
}

func cloneJob(j *Job) *Job {
	c := *j
	return &c
}
