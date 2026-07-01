package ai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/libs/collectors"
)

const (
	activityFormatterRepoChronological = "repo-chronological"
	activityFormatterJSONSignalList    = "json-signal-list"
)

// ActivityFormatter turns collected status rows into a textual body for LLM prompts and rule-based output.
type ActivityFormatter interface {
	Name() string
	Format(items []collectors.StatusItem) (string, error)
}

// SelectActivityFormatter parses ai.activity_formatter; default is repo-chronological (heading ##).
func SelectActivityFormatter(s string) ActivityFormatter {
	v := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "-"))
	switch v {
	case "", activityFormatterRepoChronological:
		return RepoChronologicalFormatter{HeadingLevel: 2}
	case activityFormatterJSONSignalList:
		return JSONSignalListFormatter{}
	default:
		// Misconfiguration should not happen if JSON Schema validates; fall back safely.
		return RepoChronologicalFormatter{HeadingLevel: 2}
	}
}

// RepoChronologicalFormatter aggregates by Repository, chronological within each repo,
// compact one line per signal. collector_error rows go to "Collector errors" at the end.
type RepoChronologicalFormatter struct {
	HeadingLevel int // 2 = "##", 3 = "###"; use 3 nested under "## Yesterday" etc.
}

func (f RepoChronologicalFormatter) Name() string { return activityFormatterRepoChronological }

func (f RepoChronologicalFormatter) headingPrefix() string {
	n := f.HeadingLevel
	if n < 2 {
		n = 2
	}
	return strings.Repeat("#", n) + " "
}

func (f RepoChronologicalFormatter) Format(items []collectors.StatusItem) (string, error) {
	if f.HeadingLevel < 2 {
		f = RepoChronologicalFormatter{HeadingLevel: 2}
	}
	var errs []collectors.StatusItem
	byRepo := map[string][]collectors.StatusItem{}
	var noRepo []collectors.StatusItem

	for _, s := range items {
		if s.Kind == "collector_error" {
			errs = append(errs, s)
			continue
		}
		r := strings.TrimSpace(s.Repository)
		if r == "" {
			noRepo = append(noRepo, s)
			continue
		}
		byRepo[r] = append(byRepo[r], s)
	}

	var b strings.Builder
	repoKeys := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repoKeys = append(repoKeys, r)
	}
	sort.Strings(repoKeys)

	for _, r := range repoKeys {
		list := byRepo[r]
		sort.Slice(list, func(i, j int) bool {
			return list[i].ObservedAt.Before(list[j].ObservedAt)
		})
		fmt.Fprintf(&b, "%s%s\n\n", f.headingPrefix(), r)
		for _, s := range list {
			line := formatSignalLine(s)
			if line != "" {
				fmt.Fprintf(&b, "%s\n", line)
			}
		}
		b.WriteString("\n")
	}

	if len(noRepo) > 0 {
		sort.Slice(noRepo, func(i, j int) bool {
			return noRepo[i].ObservedAt.Before(noRepo[j].ObservedAt)
		})
		fmt.Fprintf(&b, "%s(no repository)\n\n", f.headingPrefix())
		for _, s := range noRepo {
			line := formatSignalLine(s)
			if line != "" {
				fmt.Fprintf(&b, "%s\n", line)
			}
		}
		b.WriteString("\n")
	}

	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool {
			return errs[i].ObservedAt.Before(errs[j].ObservedAt)
		})
		fmt.Fprintf(&b, "%sCollector errors\n\n", f.headingPrefix())
		for _, e := range errs {
			did := strings.TrimSpace(e.DirectiveID)
			if did != "" {
				fmt.Fprintf(&b, "- **%s**: %s\n", did, strings.TrimSpace(e.Summary))
			} else {
				fmt.Fprintf(&b, "- **%s**: %s\n", strings.TrimSpace(e.Title), strings.TrimSpace(e.Summary))
			}
		}
	}

	return strings.TrimRight(b.String(), "\n"), nil
}

// JSONSignalListFormatter emits statuses as indented JSON array (minimal payload variant).
type JSONSignalListFormatter struct{}

func (JSONSignalListFormatter) Name() string { return activityFormatterJSONSignalList }

func (JSONSignalListFormatter) Format(items []collectors.StatusItem) (string, error) {
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// NestRepoChronologicalDepth returns a copy of fmt with Markdown heading depth bumped for nesting
// under a ## section (e.g. Yesterday / Activity). Non-repo formatters unchanged.
func NestRepoChronologicalDepth(f ActivityFormatter) ActivityFormatter {
	if rc, ok := f.(RepoChronologicalFormatter); ok {
		rc.HeadingLevel = 3
		return rc
	}
	return f
}

// formatSignalLine is a compact, model-friendly human line per StatusItem (RFC3339, source, kind, details).
func formatSignalLine(s collectors.StatusItem) string {
	src := strings.TrimSpace(s.Source)
	if src == "" {
		src = "(unknown)"
	}
	kindStr := strings.TrimSpace(s.Kind)
	if kindStr == "" {
		kindStr = "(unknown)"
	}

	iso := s.ObservedAt.Format(time.RFC3339)

	switch s.Kind {
	case "commit":
		sh := ""
		if s.Fields != nil {
			sh = s.Fields["short_hash"]
		}
		if sh == "" && s.Fields != nil {
			sh = strings.TrimSpace(s.Fields["hash"])
			if len(sh) > 7 {
				sh = sh[:7]
			}
		}
		title := strings.TrimSpace(s.Title)
		return fmt.Sprintf("%s %s commit %s: %s", iso, src, sh, title)
	case "reflog":
		return fmt.Sprintf("%s %s reflog: %s", iso, src, strings.TrimSpace(s.Title))
	case "pull_request_activity", "issue_activity":
		st := ""
		if s.Fields != nil {
			st = strings.TrimSpace(s.Fields["state"])
		}
		if s.URL != "" {
			return fmt.Sprintf("%s %s %s [%s]: %s (%s)", iso, src, kindStr, st, strings.TrimSpace(s.Title), s.URL)
		}
		return fmt.Sprintf("%s %s %s [%s]: %s", iso, src, kindStr, st, strings.TrimSpace(s.Title))
	case "authored_pr", "reviewed_pr", "involved_issue":
		st := ""
		if s.Fields != nil {
			st = strings.TrimSpace(s.Fields["state"])
		}
		title := strings.TrimSpace(s.Title)
		if s.URL != "" {
			return fmt.Sprintf("%s %s %s [%s]: %s (%s)", iso, src, kindStr, st, title, s.URL)
		}
		return fmt.Sprintf("%s %s %s [%s]: %s", iso, src, kindStr, st, title)
	case "repository_updated":
		title := strings.TrimSpace(s.Title)
		if s.URL != "" {
			return fmt.Sprintf("%s %s repository_updated: %s (%s)", iso, src, title, s.URL)
		}
		return fmt.Sprintf("%s %s repository_updated: %s", iso, src, title)
	case "calendar_event":
		title := strings.TrimSpace(s.Title)
		dur := ""
		if s.Fields != nil {
			dur = strings.TrimSpace(s.Fields["duration"])
		}
		if dur != "" {
			return fmt.Sprintf("%s %s calendar_event: %s (%s)", iso, src, title, dur)
		}
		return fmt.Sprintf("%s %s calendar_event: %s", iso, src, title)
	case "slack_dm", "slack_mention", "slack_sent",
		"slack_thread_reply", "slack_context", "slack_channel_message":
		title := strings.TrimSpace(s.Title)
		author := strings.TrimSpace(s.Author)
		actor := ""
		if author != "" {
			actor = " @" + author
		}
		if s.URL != "" {
			return fmt.Sprintf("%s %s %s%s: %s (%s)", iso, src, kindStr, actor, title, s.URL)
		}
		return fmt.Sprintf("%s %s %s%s: %s", iso, src, kindStr, actor, title)
	default:
		title := strings.TrimSpace(s.Title)
		if title == "" {
			title = strings.TrimSpace(s.Summary)
		}
		if s.URL != "" {
			return fmt.Sprintf("%s %s %s: %s (%s)", iso, src, kindStr, title, s.URL)
		}
		return fmt.Sprintf("%s %s %s: %s", iso, src, kindStr, title)
	}
}
