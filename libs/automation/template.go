package automation

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// RenderTemplate renders a Go text/template against an Event Context.
// Missing keys render as empty strings (missingkey=zero).
func RenderTemplate(tmpl string, ctx Context) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("action").Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// EnvPairs builds DOCENT_* environment variables from a Context.
func EnvPairs(ctx Context) []string {
	pairs := []struct{ k, v string }{
		{"DOCENT_RULE_ID", ctx.RuleID},
		{"DOCENT_SOURCE", ctx.Source},
		{"DOCENT_KIND", ctx.Kind},
		{"DOCENT_TITLE", ctx.Title},
		{"DOCENT_SUMMARY", ctx.Summary},
		{"DOCENT_URL", ctx.URL},
		{"DOCENT_REPO", ctx.Repo},
		{"DOCENT_BRANCH", ctx.Branch},
		{"DOCENT_OPEN_PATH", ctx.OpenPath},
		{"DOCENT_TICKET", ctx.Ticket.Key},
		{"DOCENT_TICKET_TITLE", ctx.Ticket.Title},
		{"DOCENT_TICKET_URL", ctx.Ticket.URL},
		{"DOCENT_STABLE_ID", ctx.StableID},
		{"DOCENT_FROM", ctx.From},
		{"DOCENT_TO", ctx.To},
		// Set only when an earlier action in this rule's chain already
		// failed; lets a later notifier action (e.g. a `shell` step posting
		// to Slack) report the real failure instead of guessing from side
		// effects like a missing output file.
		{"DOCENT_ACTION_ERROR", ctx.ActionError},
	}
	out := make([]string, 0, len(pairs)+len(ctx.Fields)+len(ctx.Match))
	for _, p := range pairs {
		if p.v == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s=%s", p.k, p.v))
	}
	for k, v := range ctx.Fields {
		if v == "" {
			continue
		}
		key := "DOCENT_FIELD_" + strings.ToUpper(sanitizeEnvKey(k))
		out = append(out, fmt.Sprintf("%s=%s", key, v))
	}
	for i, m := range ctx.Match {
		out = append(out, fmt.Sprintf("DOCENT_MATCH_%d=%s", i, m))
	}
	return out
}

func sanitizeEnvKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
