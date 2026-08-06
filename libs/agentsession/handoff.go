package agentsession

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// HandoffFile is written at the worktree root, where an editor opening the
// folder shows it immediately and where a prompt can reference it by name alone.
const HandoffFile = "HANDOFF.md"

// handoffTurns is how many of the most recent turns the handoff quotes in full.
// Enough to explain what just happened without pasting an hour of tool calls
// into a file a human is meant to read in twenty seconds.
const handoffTurns = 3

// handoffToolLines caps the tool list per turn, for the same reason.
const handoffToolLines = 12

// Handoff stops any running turn and writes HANDOFF.md into the session's
// worktree, returning the file's path.
//
// # Why a file
//
// Cursor exposes no way to open a new scoped chat programmatically. The only
// deeplink is cursor://anysphere.cursor-deeplink/prompt?text=, which pre-fills
// the focused window's chat box and still needs a manual send. So a promotion
// cannot transfer the conversation; it can only leave the conversation
// somewhere the next agent will find it, and point at it. That is this file.
//
// The turn is stopped first, and not as a nicety: the promoted Cursor window
// edits the same worktree, and two agents sharing a git index corrupt each
// other. Promotion means handing the worktree over, so the hosted agent lets go.
func (m *Manager) Handoff(id string) (Session, string, error) {
	sess, err := m.Get(id)
	if err != nil {
		return Session{}, "", err
	}
	if m.isLive(id) {
		_ = m.Stop(id)
		m.waitIdle(id)
		// Re-read: the stop wrote a terminal status the handoff should report.
		if s, err := m.Get(id); err == nil {
			sess = s
		}
	}
	if strings.TrimSpace(sess.Dir) == "" {
		return sess, "", fmt.Errorf("agentsession: session %s has no worktree to hand off", id)
	}
	events, _ := m.Store.Events(id)

	path := filepath.Join(sess.Dir, HandoffFile)
	if err := os.WriteFile(path, []byte(renderHandoff(sess, events)), 0o644); err != nil {
		return sess, "", fmt.Errorf("agentsession: writing %s: %w", path, err)
	}
	excludeHandoff(sess.Dir)
	return sess, path, nil
}

// excludeHandoff hides the file from git in this worktree only.
//
// Without it the first `git add -A` -- which is exactly what a promoted agent is
// likely to run -- commits the transcript summary into the branch and then the
// PR. A linked worktree has its own $GIT_DIR under .git/worktrees/<name>, and
// git reads info/exclude from there, so this affects nothing outside this
// worktree. Best-effort: a handoff that cannot be hidden is still a handoff.
func excludeHandoff(dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), gitDirTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--absolute-git-dir")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	info := filepath.Join(strings.TrimSpace(string(out)), "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return
	}
	exclude := filepath.Join(info, "exclude")
	if b, err := os.ReadFile(exclude); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == HandoffFile {
				return
			}
		}
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "\n# written by docent when an agent session is promoted\n%s\n", HandoffFile)
}

// gitDirTimeout bounds the git-dir lookup, a local config read that should take
// milliseconds.
const gitDirTimeout = 10 * time.Second

// waitIdle blocks until the cancelled turn has finished unwinding, so the file
// is written after the agent has stopped touching the worktree.
func (m *Manager) waitIdle(id string) {
	m.mu.Lock()
	lt := m.live[id]
	m.mu.Unlock()
	if lt == nil {
		return
	}
	select {
	case <-lt.done:
	case <-time.After(deleteDrainTimeout):
	}
}

// PromptText is the message to pre-fill in the promoted window's chat box. It
// names the file rather than restating the session, because the deeplink puts
// this in a URL and a whole transcript will not survive that trip.
func PromptText(sess Session) string {
	what := strings.TrimSpace(sess.Title)
	if what == "" {
		what = strings.TrimSpace(sess.Branch)
	}
	msg := "Read HANDOFF.md in this worktree. It summarizes an agent session that was running here"
	if what != "" {
		msg += " on " + what
	}
	return msg + ", including what it did and where it stopped. Pick up from there."
}

// renderHandoff turns a session and its transcript into the briefing a human or
// an agent reads on arrival: what this worktree is, what happened, and what is
// unfinished.
func renderHandoff(sess Session, events []Event) string {
	var b strings.Builder
	title := strings.TrimSpace(sess.Title)
	if title == "" {
		title = strings.TrimSpace(sess.Branch)
	}
	if title == "" {
		title = "Agent session"
	}
	fmt.Fprintf(&b, "# Handoff: %s\n\n", title)
	fmt.Fprintf(&b, "Written by docent when this session was promoted to an editor at %s.\n",
		sess.UpdatedAt.UTC().Format(time.RFC3339))
	b.WriteString("The agent that produced it has stopped, so this worktree is yours.\n\n")

	b.WriteString("## Session\n\n")
	row := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			fmt.Fprintf(&b, "- **%s**: %s\n", k, v)
		}
	}
	row("Repository", sess.Repo)
	row("Branch", sess.Branch)
	row("Worktree", sess.Dir)
	row("Provider", string(sess.Provider))
	row("Status", statusProse(sess))
	row("Turns", fmt.Sprintf("%d", sess.Turns))
	if sess.Error != "" {
		row("Last error", sess.Error)
	}
	b.WriteString("\n")

	if sess.LastResult != nil && strings.TrimSpace(sess.LastResult.Text) != "" {
		b.WriteString("## Where it left off\n\n")
		b.WriteString(strings.TrimSpace(sess.LastResult.Text))
		b.WriteString("\n\n")
	}

	turns := splitTurns(events)
	if len(turns) > handoffTurns {
		fmt.Fprintf(&b, "## Last %d turns\n\n", handoffTurns)
		fmt.Fprintf(&b, "_%d earlier turns omitted._\n\n", len(turns)-handoffTurns)
		turns = turns[len(turns)-handoffTurns:]
	} else if len(turns) > 0 {
		b.WriteString("## Conversation\n\n")
	}
	for _, t := range turns {
		writeTurn(&b, t)
	}

	b.WriteString("---\n\n")
	b.WriteString("This file is excluded from git in this worktree, so it will not end up in the PR. ")
	b.WriteString("Delete it when you no longer need it; docent rewrites it on the next promotion.\n")
	return b.String()
}

func statusProse(s Session) string {
	switch s.Status {
	case StatusRunning:
		return "was still working when it was promoted"
	case StatusIdle:
		return "finished its last turn and was waiting for a reply"
	case StatusFailed:
		return "failed"
	case StatusStopped:
		return "was stopped"
	default:
		return string(s.Status)
	}
}

// turn groups a prompt with what the agent did in response.
type turn struct {
	prompt string
	text   string
	tools  []string
}

// splitTurns re-derives turn boundaries from the flat transcript. Prompts are
// the only reliable delimiter: a turn's terminal event kind varies with how it
// ended, but every turn begins with exactly one prompt.
func splitTurns(events []Event) []turn {
	var turns []turn
	var cur *turn
	for _, ev := range events {
		switch ev.Kind {
		case KindPrompt:
			turns = append(turns, turn{prompt: ev.Text})
			cur = &turns[len(turns)-1]
		case KindText:
			if cur != nil {
				cur.text += ev.Text
			}
		case KindTool:
			if cur != nil && ev.Tool != "" {
				cur.tools = append(cur.tools, strings.TrimSpace(ev.Tool+" "+firstLine(ev.Text)))
			}
		}
	}
	return turns
}

func writeTurn(b *strings.Builder, t turn) {
	if p := strings.TrimSpace(t.prompt); p != "" {
		b.WriteString("### Asked\n\n")
		b.WriteString(quote(p))
		b.WriteString("\n")
	}
	if len(t.tools) > 0 {
		b.WriteString("Tools used: ")
		tools := t.tools
		extra := 0
		if len(tools) > handoffToolLines {
			extra = len(tools) - handoffToolLines
			tools = tools[:handoffToolLines]
		}
		b.WriteString("`" + strings.Join(tools, "`, `") + "`")
		if extra > 0 {
			fmt.Fprintf(b, " (+%d more)", extra)
		}
		b.WriteString("\n\n")
	}
	if r := strings.TrimSpace(t.text); r != "" {
		b.WriteString("### Answered\n\n")
		b.WriteString(r)
		b.WriteString("\n\n")
	}
}

func quote(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
