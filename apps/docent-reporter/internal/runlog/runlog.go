// Package runlog owns the per-run log directory that captures every
// outbound activity docent performs. A single Run anchors:
//
//   - a top-level run.log capturing the resolved mode/options and a
//     post-collection summary,
//   - one log file per enabled directive recording every HTTP request
//     and subprocess invocation the collector makes (request/response
//     sizes, status, timing), and
//   - JSON dumps of the AI provider's request and response (written
//     directly into the run dir via ai.RunInput.DebugDir).
//
// Old runs are pruned to a fixed window (PruneRunLogs).
package runlog

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultRetention is the number of run directories PruneRunLogs keeps.
const DefaultRetention = 20

// Run owns one run-log directory. It is safe for concurrent use; the
// per-directive Logger lookup and run.log writer share a single mutex.
type Run struct {
	dir       string
	runLog    *os.File
	createdAt time.Time

	mu       sync.Mutex
	loggers  map[string]*Logger
	closed   bool
}

// NewRun creates userdata/logs/<basename>/ (and the run.log inside it)
// and returns a Run handle. basename is typically the saved markdown
// output filename sans `.md` — including any `-2`/`-3` collision suffix
// the CLI resolved beforehand. now is recorded as the run's start time
// and used for the run.log header.
func NewRun(userdataDir, basename string, now time.Time) (*Run, error) {
	basename = strings.TrimSpace(basename)
	if basename == "" {
		return nil, fmt.Errorf("runlog: basename is required")
	}
	dir := filepath.Join(userdataDir, "logs", basename)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("runlog: create %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "run.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("runlog: open run.log: %w", err)
	}
	return &Run{
		dir:       dir,
		runLog:    f,
		createdAt: now,
		loggers:   map[string]*Logger{},
	}, nil
}

// Dir returns the absolute run-log directory. It doubles as the AI
// provider's DebugDir so summary-request.json / summary-response.json
// land next to the directive logs.
func (r *Run) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// RunInfo returns the writer backing run.log. Callers (the CLI)
// write the resolved options up front and a post-collection summary
// when the run finishes.
func (r *Run) RunInfo() io.Writer {
	if r == nil {
		return io.Discard
	}
	return r.runLog
}

// Directive returns the Logger for the named directive, opening
// <dir>/<directiveID>.log on first use. Subsequent calls with the same
// ID return the same Logger so multiple goroutines write to the same
// serialized file.
func (r *Run) Directive(directiveID string) *Logger {
	if r == nil {
		return nopLogger
	}
	id := sanitizeFilename(directiveID)
	if id == "" {
		return nopLogger
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nopLogger
	}
	if l, ok := r.loggers[id]; ok {
		return l
	}
	path := filepath.Join(r.dir, id+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		// Logging must never fail a real request: fall back to a
		// nop logger so the collector keeps going.
		return nopLogger
	}
	l := &Logger{w: f}
	r.loggers[id] = l
	return l
}

// Close flushes/closes every file the Run opened. Idempotent.
func (r *Run) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var firstErr error
	for _, l := range r.loggers {
		if err := l.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.runLog != nil {
		if err := r.runLog.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// PruneRunLogs keeps the `keep` most recent subdirectories of
// userdata/logs and removes the rest. Ordered by modification time
// (newest first). Errors during individual removes are swallowed so a
// failed prune never blocks a finished run.
func PruneRunLogs(userdataDir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	root := filepath.Join(userdataDir, "logs")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type runDir struct {
		path    string
		modTime time.Time
	}
	dirs := make([]runDir, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, runDir{
			path:    filepath.Join(root, e.Name()),
			modTime: info.ModTime(),
		})
	}
	if len(dirs) <= keep {
		return nil
	}
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].modTime.After(dirs[j].modTime)
	})
	for _, stale := range dirs[keep:] {
		_ = os.RemoveAll(stale.path)
	}
	return nil
}

// sanitizeFilename collapses anything that isn't [A-Za-z0-9._-] to '_'
// so directive IDs that happen to contain a separator can't escape the
// run directory.
func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// --- HTTP transport ---

// LoggingTransport is an http.RoundTripper that records timing and
// payload sizes to a Logger. The wrapped Base is used for the actual
// round trip; nil means http.DefaultTransport.
type LoggingTransport struct {
	Base   http.RoundTripper
	Logger *Logger
}

// RoundTrip records the request/response sizes, status, and duration
// before returning. Errors are forwarded verbatim so callers can
// continue to inspect them.
func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	logger := t.Logger
	if logger == nil {
		logger = nopLogger
	}
	reqBytes := contentLength(req.Header, req.ContentLength)
	start := time.Now()
	res, err := base.RoundTrip(req)
	duration := time.Since(start)
	status := 0
	resBytes := int64(-1)
	if res != nil {
		status = res.StatusCode
		resBytes = res.ContentLength
	}
	logger.LogHTTP(req.Method, redactURL(req.URL.String()), reqBytes, status, resBytes, duration, err)
	return res, err
}

// WrapHTTPClient returns an http.Client whose transport is wrapped to
// log to logger. If client is nil, http.DefaultClient is used as the
// template (preserving its timeout/cookie behavior). If logger is nil
// or already a nop, client is returned unchanged.
func WrapHTTPClient(client *http.Client, logger *Logger) *http.Client {
	if logger == nil || logger == nopLogger {
		if client == nil {
			return http.DefaultClient
		}
		return client
	}
	base := http.DefaultTransport
	template := client
	if template == nil {
		template = http.DefaultClient
	}
	if template.Transport != nil {
		base = template.Transport
	}
	return &http.Client{
		Transport:     &LoggingTransport{Base: base, Logger: logger},
		CheckRedirect: template.CheckRedirect,
		Jar:           template.Jar,
		Timeout:       template.Timeout,
	}
}

func contentLength(h http.Header, declared int64) int {
	if declared > 0 {
		return int(declared)
	}
	if v := strings.TrimSpace(h.Get("Content-Length")); v != "" {
		var n int
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	return 0
}
