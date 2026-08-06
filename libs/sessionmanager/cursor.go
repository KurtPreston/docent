package sessionmanager

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/KurtPreston/docent/libs/wmclient"
)

// CursorManager lists and opens Cursor windows via the Cursor CLI. It lists
// with `cursor --status` (which forwards to the local Cursor GUI over the
// remote-cli IPC socket) and produces `cursor://` deep links for the frontend.
//
// Focus of a specific existing window is not supported from the backend — the
// frontend navigates the DeepLink instead (see ErrFocusUnsupported).
type CursorManager struct {
	// Command is the Cursor CLI binary (default "cursor").
	Command string
	// Host is the default ssh alias used when building remote deep links and
	// when opening a remote folder. Overridden per-call when a host is given.
	Host string

	// run overrides command execution; injected in tests. When nil, the real
	// binary is exec'd.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (m *CursorManager) Provider() string { return "cursor" }

func (m *CursorManager) command() string {
	if strings.TrimSpace(m.Command) != "" {
		return m.Command
	}
	return "cursor"
}

func (m *CursorManager) runner() func(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.run != nil {
		return m.run
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
}

// List runs `cursor --status` and parses the reported windows.
func (m *CursorManager) List(ctx context.Context) ([]Session, error) {
	out, err := m.runner()(ctx, m.command(), "--status")
	if err != nil {
		return nil, fmt.Errorf("cursor --status: %w", err)
	}
	return ParseStatus(string(out)), nil
}

// Open opens (or reveals) the folder at req.Path via `cursor --folder-uri`.
// This only reliably reaches a Cursor GUI when docent runs co-located with it;
// the primary open path in the remote-daemon topology is the client-side deep
// link. Focus is best-effort: opening an already-open folder reveals it.
func (m *CursorManager) Open(ctx context.Context, req OpenReq) error {
	uri := folderURI(req.Path, firstNonEmpty(req.Host, m.Host))
	if uri == "" {
		return fmt.Errorf("cursor: open requires a path")
	}
	if _, err := m.runner()(ctx, m.command(), "--folder-uri", uri); err != nil {
		return fmt.Errorf("cursor --folder-uri %s: %w", uri, err)
	}
	return nil
}

// Focus is not supported from the backend for Cursor; callers should navigate
// the DeepLink instead.
func (m *CursorManager) Focus(ctx context.Context, req FocusReq) error {
	return ErrFocusUnsupported
}

// DeepLink builds a cursor:// URI that opens/reveals path (on ssh host when
// set). Returns "" when path is empty.
//
// The authority it synthesizes ("ssh-remote+<host>") is the right one for a
// folder no window has open, but it is a guess: see DeepLinkAuthority for why a
// window that is already open needs its own spelling instead.
func (m *CursorManager) DeepLink(path, host string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	host = firstNonEmpty(strings.TrimSpace(host), m.Host)
	if host != "" {
		return "cursor://vscode-remote/ssh-remote+" + host + ensureLeadingSlash(path)
	}
	return "cursor://file" + ensureLeadingSlash(path)
}

// DeepLinkAuthority builds a cursor:// URI addressed to the remote authority a
// window reports for itself, falling back to DeepLink when that authority is
// unknown.
//
// Cursor decides whether a deep link means "reveal that window" or "open a new
// one" by comparing whole workspace URIs, authority included. Recent builds
// encode the ssh target as hex-encoded JSON rather than a bare alias — a window
// on the "desktop" alias reports
// ssh-remote+7b22686f73744e616d65223a226465736b746f70227d, which is
// {"hostName":"desktop"} — so the ssh-remote+desktop form DeepLink synthesizes
// is a different workspace to Cursor even though it resolves to the same folder
// on the same box. The result was two windows on one folder, identical down to
// the title bar. Echoing the authority back verbatim avoids guessing at an
// encoding that is not ours to define.
func (m *CursorManager) DeepLinkAuthority(path, authority, host string) string {
	path = strings.TrimSpace(path)
	authority = strings.TrimSpace(authority)
	if path == "" || authority == "" {
		return m.DeepLink(path, host)
	}
	return "cursor://vscode-remote/" + authority + ensureLeadingSlash(path)
}

// folderURI builds the --folder-uri argument for opening a folder.
func folderURI(path, host string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if host != "" {
		return "vscode-remote://ssh-remote+" + host + ensureLeadingSlash(path)
	}
	return "file://" + ensureLeadingSlash(path)
}

// windowLineRe matches a `cursor --status` process row for an editor window,
// e.g. `    0    624   14972    window [3] (whip.tsx - branch [SSH: host] - Cursor)`.
var windowLineRe = regexp.MustCompile(`window \[(\d+)\]\s*\((.*)\)\s*$`)

// ParseStatus extracts editor windows from `cursor --status` output. The status
// text is a human-readable table; the window rows carry `window [N] (<title>)`
// in the Process column, which is stable across Cursor/VS Code versions.
func ParseStatus(out string) []Session {
	var sessions []Session
	for _, line := range strings.Split(out, "\n") {
		m := windowLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		title := strings.TrimSpace(m[2])
		leaf, host := wmclient.ParseCursorTitle(title)
		sessions = append(sessions, Session{
			ID:    m[1],
			Title: title,
			Leaf:  leaf,
			Host:  host,
			App:   "Cursor",
		})
	}
	return sessions
}

func ensureLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
