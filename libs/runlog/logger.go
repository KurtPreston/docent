package runlog

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Logger records HTTP request/response and subprocess invocation
// metadata to a single underlying writer. Concurrent writes are
// serialized by the embedded mutex so log lines never interleave.
type Logger struct {
	w  io.WriteCloser
	mu sync.Mutex
}

// nopLogger is returned when the caller didn't configure a Logger or
// when a non-critical error (e.g. permission denied opening the per-
// directive file) means we'd otherwise have to fail the run.
var nopLogger = &Logger{}

// LogHTTP records a single HTTP request/response. err is non-nil for
// transport-level failures (no response was received); status, reqBytes
// and resBytes are tolerated as -1/0 when unknown.
func (l *Logger) LogHTTP(method, urlStr string, reqBytes int, status int, resBytes int64, duration time.Duration, err error) {
	if l == nil || l.w == nil {
		return
	}
	var sb strings.Builder
	sb.Grow(160)
	sb.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
	sb.WriteString(" HTTP ")
	sb.WriteString(strings.ToUpper(strings.TrimSpace(method)))
	sb.WriteString(" ")
	sb.WriteString(urlStr)
	if status > 0 {
		fmt.Fprintf(&sb, " status=%d", status)
	}
	fmt.Fprintf(&sb, " req=%s", formatBytes(int64(reqBytes)))
	if resBytes >= 0 {
		fmt.Fprintf(&sb, " res=%s", formatBytes(resBytes))
	}
	fmt.Fprintf(&sb, " duration=%s", duration.Round(time.Microsecond))
	if err != nil {
		fmt.Fprintf(&sb, " error=%q", err.Error())
	}
	sb.WriteByte('\n')
	l.write(sb.String())
}

// LogExec records a single subprocess invocation. stdoutBytes /
// stderrBytes are the lengths of the captured streams; pass 0 when
// nothing was captured.
func (l *Logger) LogExec(name string, args []string, exitCode int, stdoutBytes, stderrBytes int, duration time.Duration, err error) {
	if l == nil || l.w == nil {
		return
	}
	var sb strings.Builder
	sb.Grow(160)
	sb.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
	sb.WriteString(" EXEC ")
	sb.WriteString(name)
	for _, a := range args {
		sb.WriteByte(' ')
		sb.WriteString(redactArg(a))
	}
	fmt.Fprintf(&sb, " exit=%d stdout=%s stderr=%s duration=%s",
		exitCode,
		formatBytes(int64(stdoutBytes)),
		formatBytes(int64(stderrBytes)),
		duration.Round(time.Microsecond),
	)
	if err != nil {
		fmt.Fprintf(&sb, " error=%q", err.Error())
	}
	sb.WriteByte('\n')
	l.write(sb.String())
}

// Note records a free-form note in the directive log. Useful for retry
// markers or "falling back to X" breadcrumbs that don't fit the HTTP /
// EXEC schemas.
func (l *Logger) Note(format string, args ...any) {
	if l == nil || l.w == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s NOTE %s\n", time.Now().UTC().Format(time.RFC3339Nano), strings.TrimRight(msg, "\n"))
	l.write(line)
}

func (l *Logger) write(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		return
	}
	_, _ = io.WriteString(l.w, line)
}

func (l *Logger) close() error {
	if l == nil || l.w == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.w.Close()
	l.w = nil
	return err
}

// formatBytes renders sizes with an SI-style suffix so log lines stay
// readable. Negative values (unknown response length) collapse to "?".
func formatBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1fGB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1fMB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.1fKB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// redactURL strips obviously-sensitive query parameters from a URL.
// Google iCal secret URLs use opaque path tokens we can't safely
// redact without breaking the identity of the request, so we only
// redact known sensitive query keys.
func redactURL(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "REDACTED")
	}
	q := u.Query()
	changed := false
	for k := range q {
		switch strings.ToLower(k) {
		case "token", "access_token", "auth", "authorization", "key", "api_key", "apikey", "password":
			q.Set(k, "REDACTED")
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// redactArg strips inline `KEY=value` pairs that look like secret
// material (TOKEN, KEY, SECRET, PASSWORD) so that env=... arguments
// captured from exec invocations don't leak credentials.
func redactArg(arg string) string {
	if i := strings.IndexByte(arg, '='); i > 0 {
		key := arg[:i]
		upper := strings.ToUpper(key)
		switch {
		case strings.Contains(upper, "TOKEN"),
			strings.Contains(upper, "SECRET"),
			strings.Contains(upper, "PASSWORD"),
			strings.HasSuffix(upper, "_KEY"),
			upper == "AUTHORIZATION":
			return key + "=REDACTED"
		}
	}
	return arg
}
