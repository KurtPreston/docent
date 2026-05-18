package collectors

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// doAndReadHTTP issues req through client (or http.DefaultClient when
// nil), buffers the response body up to maxBytes, and records the
// resulting timing + sizes against the directive logger derived from
// opts. The buffered body is returned alongside the response (with
// res.Body already closed) so callers can inspect headers (e.g. for
// Retry-After) without having to replay the read themselves.
//
// On transport-level errors, res is nil and body is empty. On non-2xx
// responses the body is still buffered so callers can surface a useful
// error snippet.
func doAndReadHTTP(client *http.Client, req *http.Request, maxBytes int64, opts *CollectOpts, directiveID string) (*http.Response, []byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	logger := loggerFor(opts, directiveID)
	reqBytes := requestBodySize(req)
	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		logger.LogHTTP(req.Method, req.URL.String(), reqBytes, 0, -1, time.Since(start), err)
		return nil, nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(res.Body, maxBytes))
	_ = res.Body.Close()
	duration := time.Since(start)
	logger.LogHTTP(req.Method, req.URL.String(), reqBytes, res.StatusCode, int64(len(body)), duration, readErr)
	if readErr != nil {
		return res, body, readErr
	}
	// Re-attach the buffered body so callers that still expect to read
	// from res.Body (rare, but allowed) keep working.
	res.Body = io.NopCloser(bytes.NewReader(body))
	return res, body, nil
}

// requestBodySize returns a best-effort byte count for req.Body. It
// avoids consuming the body — Content-Length / req.ContentLength are
// consulted first, falling back to 0 when neither is set.
func requestBodySize(req *http.Request) int {
	if req == nil {
		return 0
	}
	if req.ContentLength > 0 {
		return int(req.ContentLength)
	}
	if v := req.Header.Get("Content-Length"); v != "" {
		var n int
		for _, r := range v {
			if r < '0' || r > '9' {
				return 0
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	return 0
}

// runAndLogExec executes cmd, captures stdout and stderr to in-memory
// buffers, and records timing / exit code against the directive
// logger. Use this in place of cmd.Output() / cmd.CombinedOutput() at
// any subprocess call site that should appear in the run log.
//
// Returns the captured stdout (so callers can keep using it as if they
// had called cmd.Output) and any execution error. When the process
// exits non-zero, the returned error is wrapped (mirroring exec.Cmd's
// own behavior) and the *exec.ExitError it contains is populated with
// cmd.Stderr's contents.
func runAndLogExec(cmd *exec.Cmd, opts *CollectOpts, directiveID string) ([]byte, error) {
	logger := loggerFor(opts, directiveID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	logger.LogExec(cmd.Path, execArgs(cmd), exitCode, stdout.Len(), stderr.Len(), duration, err)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.Stderr == nil {
			exitErr.Stderr = stderr.Bytes()
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// runAndLogExecContext is a thin convenience over runAndLogExec for
// callers that haven't built a *exec.Cmd yet.
func runAndLogExecContext(ctx context.Context, name string, args []string, dir string, env []string, opts *CollectOpts, directiveID string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	return runAndLogExec(cmd, opts, directiveID)
}

func execArgs(cmd *exec.Cmd) []string {
	if cmd == nil || len(cmd.Args) <= 1 {
		return nil
	}
	out := make([]string, len(cmd.Args)-1)
	copy(out, cmd.Args[1:])
	return out
}
