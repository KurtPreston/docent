package automation

import (
	"context"
	"fmt"
	"strings"
)

// IssueCommenter posts a comment on an issue tracker item (e.g. JIRA).
type IssueCommenter interface {
	PostComment(ctx context.Context, issueKey, body string) error
}

// IssueCommenterFunc adapts a function to IssueCommenter.
type IssueCommenterFunc func(ctx context.Context, issueKey, body string) error

func (f IssueCommenterFunc) PostComment(ctx context.Context, issueKey, body string) error {
	return f(ctx, issueKey, body)
}

// ChatPoster posts a message to a chat channel (e.g. Slack).
type ChatPoster interface {
	PostMessage(ctx context.Context, channel, body string) error
}

// ChatPosterFunc adapts a function to ChatPoster.
type ChatPosterFunc func(ctx context.Context, channel, body string) error

func (f ChatPosterFunc) PostMessage(ctx context.Context, channel, body string) error {
	return f(ctx, channel, body)
}

// JiraCommentRunner posts a templated comment via IssueCommenter.
type JiraCommentRunner struct {
	Commenter IssueCommenter
}

func (r JiraCommentRunner) Run(ctx context.Context, action Action, ev Event) error {
	if r.Commenter == nil {
		return fmt.Errorf("jira-comment: no commenter configured")
	}
	actx := EventContext(ev)
	issueTmpl := action.Issue
	if strings.TrimSpace(issueTmpl) == "" {
		issueTmpl = "{{.Ticket.Key}}"
	}
	issue, err := RenderTemplate(issueTmpl, actx)
	if err != nil {
		return err
	}
	issue = strings.TrimSpace(issue)
	if issue == "" && actx.Ticket.Key != "" {
		issue = actx.Ticket.Key
	}
	if issue == "" {
		return fmt.Errorf("jira-comment: issue key is empty after templating")
	}
	body, err := RenderTemplate(action.Body, actx)
	if err != nil {
		return err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("jira-comment: body is empty after templating")
	}
	return r.Commenter.PostComment(ctx, issue, body)
}

// SlackPostRunner posts a templated message via ChatPoster.
type SlackPostRunner struct {
	Poster ChatPoster
}

func (r SlackPostRunner) Run(ctx context.Context, action Action, ev Event) error {
	if r.Poster == nil {
		return fmt.Errorf("slack-post: no poster configured")
	}
	actx := EventContext(ev)
	channelTmpl := action.Channel
	if strings.TrimSpace(channelTmpl) == "" {
		// Fall back to the signal's channel field when present.
		if actx.Fields != nil {
			channelTmpl = actx.Fields["channel_id"]
			if channelTmpl == "" {
				channelTmpl = actx.Fields["channel"]
			}
		}
	}
	channel, err := RenderTemplate(channelTmpl, actx)
	if err != nil {
		return err
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return fmt.Errorf("slack-post: channel is empty after templating")
	}
	body, err := RenderTemplate(action.Body, actx)
	if err != nil {
		return err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("slack-post: body is empty after templating")
	}
	return r.Poster.PostMessage(ctx, channel, body)
}
