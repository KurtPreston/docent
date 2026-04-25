package daybook

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/collectors"
)

type Entry struct {
	Path    string
	Content string
}

func Path(root string, date time.Time) string {
	return filepath.Join(root, "daybook", date.Format("2006-01-02")+".md")
}

func LoadOrCreate(root string, date time.Time) (Entry, error) {
	path := Path(root, date)
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		initial := fmt.Sprintf("# %s\n\n## Plan\n\n## Status Updates\n\n## Delegation Candidates\n\n## Reflection\n", date.Format("2006-01-02"))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Entry{}, err
		}
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			return Entry{}, err
		}
		return Entry{Path: path, Content: initial}, nil
	}
	if err != nil {
		return Entry{}, err
	}
	return Entry{Path: path, Content: string(content)}, nil
}

func AppendStatus(entry Entry, statuses []collectors.StatusItem) error {
	var buf bytes.Buffer
	buf.WriteString(strings.TrimRight(entry.Content, "\n"))
	buf.WriteString("\n\n## Status Snapshot\n\n")
	if len(statuses) == 0 {
		buf.WriteString("- No status updates were collected.\n")
	} else {
		for _, status := range statuses {
			var tag string
			if status.AttentionClass != "" || status.ChangeState != "" {
				tag = fmt.Sprintf("[%s/%s] ", status.AttentionClass, status.ChangeState)
			}
			buf.WriteString(fmt.Sprintf("- `%s` %s%s: %s", status.Source, tag, status.Title, status.Summary))
			if status.URL != "" {
				buf.WriteString(" (" + status.URL + ")")
			}
			buf.WriteByte('\n')
		}
	}
	return os.WriteFile(entry.Path, buf.Bytes(), 0o644)
}

func AppendPlan(entry Entry, output ai.PlanningOutput) error {
	var buf bytes.Buffer
	buf.WriteString(strings.TrimRight(entry.Content, "\n"))
	buf.WriteString("\n\n## AI Plan\n\n")
	buf.WriteString(output.Summary)
	buf.WriteString("\n\n### Focus Blocks\n\n")
	if len(output.FocusBlocks) == 0 {
		buf.WriteString("- No focus blocks proposed.\n")
	}
	for _, block := range output.FocusBlocks {
		label := block.Title
		if block.TaskID != "" {
			label = "`" + block.TaskID + "` " + label
		}
		buf.WriteString("- " + label)
		if block.Reason != "" {
			buf.WriteString(": " + block.Reason)
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("\n### Delegation Candidates\n\n")
	if len(output.DelegationCandidates) == 0 {
		buf.WriteString("- No delegation candidates proposed.\n")
	}
	for _, candidate := range output.DelegationCandidates {
		label := candidate.Title
		if candidate.TaskID != "" {
			label = "`" + candidate.TaskID + "` " + label
		}
		buf.WriteString("- " + label + ": " + candidate.Reason + "\n")
		if candidate.SuggestedPrompt != "" {
			buf.WriteString("  Prompt: " + candidate.SuggestedPrompt + "\n")
		}
	}
	return os.WriteFile(entry.Path, buf.Bytes(), 0o644)
}

func AppendReflection(entry Entry, output ai.PlanningOutput) error {
	var buf bytes.Buffer
	buf.WriteString(strings.TrimRight(entry.Content, "\n"))
	buf.WriteString("\n\n## End-of-Day Reflection Prompt\n\n")
	buf.WriteString(output.Summary)
	buf.WriteString("\n\n")
	for _, question := range output.Questions {
		buf.WriteString("- " + question + "\n")
	}
	return os.WriteFile(entry.Path, buf.Bytes(), 0o644)
}
