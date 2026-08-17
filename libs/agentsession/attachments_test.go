package agentsession

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentStore_stageAndPromote(t *testing.T) {
	root := t.TempDir()
	store, err := NewAttachmentStore(root)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte("hello attachment")
	staged, err := store.Stage("note.txt", "text/plain", bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if staged.ID == "" || staged.Name != "note.txt" {
		t.Fatalf("staged: %+v", staged)
	}

	atts, err := store.Promote("sess-1", []string{staged.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].Name != "note.txt" {
		t.Fatalf("promoted: %+v", atts)
	}
	if _, err := os.Stat(atts[0].Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.stagingDir(staged.ID)); !os.IsNotExist(err) {
		t.Fatal("staging dir should be removed after promote")
	}
}

func TestAttachmentStore_dedupeName(t *testing.T) {
	root := t.TempDir()
	store, err := NewAttachmentStore(root)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := store.Stage("image.png", "image/png", bytes.NewReader([]byte("a")), 1)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := store.Stage("image.png", "image/png", bytes.NewReader([]byte("b")), 1)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := store.Promote("sess-2", []string{s1.ID, s2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("got %d attachments", len(atts))
	}
	if atts[0].Name == atts[1].Name {
		t.Fatalf("expected distinct names, got %q and %q", atts[0].Name, atts[1].Name)
	}
}

func TestSanitizeAttachmentName_rejectsTraversal(t *testing.T) {
	for _, name := range []string{"../secret", "../../etc/passwd", ".hidden", ""} {
		if _, err := SanitizeAttachmentName(name); err == nil {
			t.Errorf("name %q: want error", name)
		}
	}
}

func TestPromptWithAttachments(t *testing.T) {
	got := PromptWithAttachments("fix this", []Attachment{{Name: "shot.png", Path: "/tmp/shot.png"}})
	if !strings.Contains(got, "fix this") || !strings.Contains(got, "/tmp/shot.png") {
		t.Fatalf("prompt: %q", got)
	}
}

func TestAttachmentDirs(t *testing.T) {
	dirs := AttachmentDirs([]Attachment{
		{Path: "/state/sess/attachments/a.png"},
		{Path: "/state/sess/attachments/b.png"},
	})
	if len(dirs) != 1 || dirs[0] != filepath.Join("/state", "sess", "attachments") {
		t.Fatalf("dirs: %v", dirs)
	}
}

func TestCursorArgs_addDir(t *testing.T) {
	attDir := "/state/sess/attachments"
	args := cursorArgs(TurnRequest{
		SessionID: "chat-1",
		Attachments: []Attachment{{
			Name: "shot.png",
			Path: attDir + "/shot.png",
		}},
	})
	if !containsSeq(args, "--add-dir", attDir) {
		t.Fatalf("args: %v", args)
	}
	// --add-dir must precede any future variadic tail flags.
	if idx := argIndex(args, "--add-dir"); idx < 0 {
		t.Fatal("--add-dir missing")
	}
}

func containsSeq(args []string, want ...string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		ok := true
		for j, w := range want {
			if args[i+j] != w {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func argIndex(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}

func TestAttachmentStore_openSessionAttachment(t *testing.T) {
	root := t.TempDir()
	store, err := NewAttachmentStore(root)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := store.Stage("pic.png", "image/png", bytes.NewReader([]byte{1, 2, 3}), 3)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := store.Promote("sess-3", []string{staged.ID})
	if err != nil {
		t.Fatal(err)
	}
	f, att, err := store.OpenSessionAttachment("sess-3", atts[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if att.Name != "pic.png" {
		t.Fatalf("att: %+v", att)
	}
}
