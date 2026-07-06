package sessionmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// realStatus is trimmed-but-faithful `cursor --status` output captured live,
// including the noise rows we must ignore and two remote windows we must parse.
const realStatus = `
Version:          Cursor 3.10.17 (c89f45b, 2026-07-05T06:39:45.228Z)
OS Version:       Windows_NT x64 10.0.22631

CPU %	Mem MB	   PID	Process
    0	   212	 29356	cursor main
    0	   101	  2160	fileWatcher [1]
    0	   199	  5388	extensionHost [6]
    0	    99	  7484	     electron-nodejs (gitWorker.js )
    0	   624	 14972	window [3] (whipTopicInput.tsx - salsa-12656-topic-not-found [SSH: desktop] - Cursor)
    0	   709	 15536	window [6] (fillsService.ts - as_gui-4.0 [SSH: desktop] - Cursor)
`

func TestParseStatus(t *testing.T) {
	got := ParseStatus(realStatus)
	if len(got) != 2 {
		t.Fatalf("want 2 windows, got %d: %+v", len(got), got)
	}
	want := []Session{
		{ID: "3", Title: "whipTopicInput.tsx - salsa-12656-topic-not-found [SSH: desktop] - Cursor", Leaf: "salsa-12656-topic-not-found", Host: "desktop", App: "Cursor"},
		{ID: "6", Title: "fillsService.ts - as_gui-4.0 [SSH: desktop] - Cursor", Leaf: "as_gui-4.0", Host: "desktop", App: "Cursor"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("window %d:\n got  %+v\n want %+v", i, got[i], w)
		}
	}
}

func TestParseStatusLocalWindow(t *testing.T) {
	out := "    0	   500	  111	window [1] (main.go - my-project - Cursor)\n"
	got := ParseStatus(out)
	if len(got) != 1 {
		t.Fatalf("want 1 window, got %d", len(got))
	}
	if got[0].Leaf != "my-project" || got[0].Host != "" {
		t.Errorf("local window parse: got leaf=%q host=%q", got[0].Leaf, got[0].Host)
	}
}

func TestParseStatusNoWindows(t *testing.T) {
	if got := ParseStatus("no windows here\nextensionHost [6]\n"); len(got) != 0 {
		t.Fatalf("want 0 windows, got %d", len(got))
	}
}

func TestCursorDeepLink(t *testing.T) {
	cases := []struct {
		name string
		mgr  CursorManager
		path string
		host string
		want string
	}{
		{"remote explicit host", CursorManager{}, "/home/me/proj", "desktop", "cursor://vscode-remote/ssh-remote+desktop/home/me/proj"},
		{"remote default host", CursorManager{Host: "devbox"}, "/home/me/proj", "", "cursor://vscode-remote/ssh-remote+devbox/home/me/proj"},
		{"local", CursorManager{}, "/home/me/proj", "", "cursor://file/home/me/proj"},
		{"relative path gets slash", CursorManager{}, "home/me/proj", "", "cursor://file/home/me/proj"},
		{"empty path", CursorManager{}, "", "desktop", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.mgr.DeepLink(tc.path, tc.host); got != tc.want {
				t.Errorf("DeepLink(%q,%q) = %q, want %q", tc.path, tc.host, got, tc.want)
			}
		})
	}
}

func TestCursorOpenBuildsFolderURI(t *testing.T) {
	cases := []struct {
		name    string
		mgr     CursorManager
		req     OpenReq
		wantArg string
	}{
		{"remote", CursorManager{Command: "cursor"}, OpenReq{Path: "/home/me/proj", Host: "desktop"}, "vscode-remote://ssh-remote+desktop/home/me/proj"},
		{"local", CursorManager{Command: "cursor"}, OpenReq{Path: "/home/me/proj"}, "file:///home/me/proj"},
		{"default host", CursorManager{Command: "cursor", Host: "devbox"}, OpenReq{Path: "/home/me/proj"}, "vscode-remote://ssh-remote+devbox/home/me/proj"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotArgs []string
			var gotName string
			tc.mgr.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				gotName = name
				gotArgs = args
				return nil, nil
			}
			if err := tc.mgr.Open(context.Background(), tc.req); err != nil {
				t.Fatal(err)
			}
			if gotName != "cursor" {
				t.Errorf("command = %q, want cursor", gotName)
			}
			if len(gotArgs) != 2 || gotArgs[0] != "--folder-uri" || gotArgs[1] != tc.wantArg {
				t.Errorf("args = %v, want [--folder-uri %s]", gotArgs, tc.wantArg)
			}
		})
	}
}

func TestCursorOpenRequiresPath(t *testing.T) {
	m := CursorManager{}
	if err := m.Open(context.Background(), OpenReq{}); err == nil {
		t.Fatal("expected error opening with empty path")
	}
}

func TestCursorFocusUnsupported(t *testing.T) {
	m := CursorManager{}
	if err := m.Focus(context.Background(), FocusReq{Name: "x"}); !errors.Is(err, ErrFocusUnsupported) {
		t.Fatalf("want ErrFocusUnsupported, got %v", err)
	}
}

func TestSelect(t *testing.T) {
	if mgr := Select(userdata.SessionManagerConfig{}); mgr != nil {
		t.Errorf("empty provider: want nil, got %T", mgr)
	}
	if mgr := Select(userdata.SessionManagerConfig{Provider: "tmux"}); mgr != nil {
		t.Errorf("unknown provider: want nil, got %T", mgr)
	}

	cur, ok := Select(userdata.SessionManagerConfig{Provider: "cursor"}).(*CursorManager)
	if !ok {
		t.Fatal("cursor provider: want *CursorManager")
	}
	if cur.Command != "cursor" {
		t.Errorf("cursor default command = %q, want cursor", cur.Command)
	}

	cur2 := Select(userdata.SessionManagerConfig{
		Provider: "cursor",
		Cursor:   userdata.SessionManagerCursor{Command: "my-cursor", Host: "box"},
	}).(*CursorManager)
	if cur2.Command != "my-cursor" || cur2.Host != "box" {
		t.Errorf("cursor overrides not applied: %+v", cur2)
	}

	w, ok := Select(userdata.SessionManagerConfig{Provider: "wsm"}).(*WSMManager)
	if !ok {
		t.Fatal("wsm provider: want *WSMManager")
	}
	if w.Provider() != "wsm" {
		t.Errorf("wsm Provider() = %q", w.Provider())
	}
}
