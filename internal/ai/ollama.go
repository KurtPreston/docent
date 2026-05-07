package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type OllamaProvider struct {
	BaseURL    string
	Model      string
	HTTPClient *http.Client
	Formatter  ActivityFormatter
}

type ollamaChatRequest struct {
	Model    string      `json:"model"`
	Messages []ollamaMsg `json:"messages"`
	Stream   bool        `json:"stream"`
	Format   string      `json:"format,omitempty"`
}

type ollamaMsg struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	Thinking string `json:"thinking,omitempty"`
}

type ollamaChatStreamResponse struct {
	Message         ollamaMsg `json:"message"`
	Done            bool      `json:"done"`
	DoneReason      string    `json:"done_reason,omitempty"`
	PromptEvalCount int       `json:"prompt_eval_count,omitempty"`
	EvalCount       int       `json:"eval_count,omitempty"`
}

func (p OllamaProvider) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return http.DefaultClient
}

func (p OllamaProvider) formatterOrDefault() ActivityFormatter {
	if p.Formatter != nil {
		return p.Formatter
	}
	return SelectActivityFormatter("")
}

func (p OllamaProvider) GenerateDailyPlan(ctx context.Context, in DailyPlanInput) (string, error) {
	instruction := "Create a practical daily plan. Section `## Yesterday` summarizes factual work from the aggregated activity below. Section `## Today` proposes a focused plan for today using that activity and optional priorities."
	payload, err := BuildDailyPlanPrompt(instruction, in, p.formatterOrDefault())
	if err != nil {
		return "", err
	}
	p.printPrompt(in.StreamOut, payload)
	raw, err := p.chatMarkdown(ctx, payload, in.DebugDir, in.StreamOut, "markdown-request")
	if err != nil {
		return "", err
	}
	return StripMarkdownFence(raw), nil
}

func (p OllamaProvider) SummarizeRecentActivity(ctx context.Context, in RecentActivityInput) (string, error) {
	instruction := fmt.Sprintf(
		"Summarize the developer's recent activity over %d calendar day(s) (%s to %s). Activity below is grouped by Git repository where each item's repository field is set (usually org/repo); treat it as ground truth. Return one Markdown document with a brief executive summary at the top and noteworthy callouts. Do not invent activity not present in the input.",
		in.LookbackDays,
		in.Since.Format(time.RFC3339),
		in.Now.Format(time.RFC3339),
	)
	payload, err := BuildRecentActivityPrompt(instruction, in, p.formatterOrDefault())
	if err != nil {
		return "", err
	}
	p.printPrompt(in.StreamOut, payload)
	raw, err := p.chatMarkdown(ctx, payload, in.DebugDir, in.StreamOut, "recent-activity-request")
	if err != nil {
		return "", err
	}
	return StripMarkdownFence(raw), nil
}

func (p OllamaProvider) RunCustomPrompt(ctx context.Context, in CustomPromptInput) (string, error) {
	payload, err := BuildCustomPromptPayload(in.UserPrompt, in, p.formatterOrDefault())
	if err != nil {
		return "", err
	}
	p.printPrompt(in.StreamOut, payload)
	raw, err := p.chatMarkdown(ctx, payload, in.DebugDir, in.StreamOut, "custom-prompt-request")
	if err != nil {
		return "", err
	}
	return StripMarkdownFence(raw), nil
}

func (p OllamaProvider) printPrompt(streamOut io.Writer, payload string) {
	if streamOut == nil {
		return
	}
	fmt.Fprintln(streamOut, "Prompt:")
	fmt.Fprintln(streamOut, strings.Repeat("-", 72))
	_, _ = io.WriteString(streamOut, payload)
	fmt.Fprintln(streamOut)
	fmt.Fprintln(streamOut, strings.Repeat("-", 72))
	fmt.Fprintln(streamOut)
}

func (p OllamaProvider) chatMarkdown(ctx context.Context, userContent, debugDir string, streamOut io.Writer, requestLogStage string) (string, error) {
	body, err := json.Marshal(ollamaChatRequest{
		Model: p.Model,
		Messages: []ollamaMsg{
			{Role: "user", Content: userContent},
		},
		Stream: true,
	})
	if err != nil {
		return "", err
	}
	if debugDir != "" {
		writeOllamaDebugLog(debugDir, requestLogStage, map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"base_url":  p.BaseURL,
			"model":     p.Model,
			"request":   json.RawMessage(body),
			"prompt":    userContent,
		})
	}
	url := strings.TrimRight(p.BaseURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(res.Body)
		if debugDir != "" {
			writeOllamaDebugLog(debugDir, "error", map[string]any{
				"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
				"status":      res.Status,
				"status_code": res.StatusCode,
				"response":    strings.TrimSpace(errBody.String()),
			})
		}
		return "", fmt.Errorf("ollama %s: %s", res.Status, strings.TrimSpace(errBody.String()))
	}
	decoder := json.NewDecoder(res.Body)
	var content strings.Builder
	streamChunks := make([]ollamaChatStreamResponse, 0, 128)
	for {
		var chunk ollamaChatStreamResponse
		if err := decoder.Decode(&chunk); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if debugDir != "" {
				writeOllamaDebugLog(debugDir, "error", map[string]any{
					"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
					"error":     fmt.Sprintf("ollama stream parse: %v", err),
				})
			}
			return "", fmt.Errorf("ollama stream: %w", err)
		}
		streamChunks = append(streamChunks, chunk)
		if chunk.Message.Thinking != "" && streamOut != nil {
			_, _ = io.WriteString(streamOut, chunk.Message.Thinking)
		}
		if chunk.Message.Content != "" {
			content.WriteString(chunk.Message.Content)
			if streamOut != nil {
				_, _ = io.WriteString(streamOut, chunk.Message.Content)
			}
		}
		if chunk.Done {
			break
		}
	}
	out := content.String()
	if debugDir != "" {
		writeOllamaDebugLog(debugDir, "markdown-response", map[string]any{
			"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
			"status":          res.Status,
			"status_code":     res.StatusCode,
			"response_raw":    streamChunks,
			"message_content": out,
		})
	}
	return out, nil
}

func writeOllamaDebugLog(debugDir, stage string, payload map[string]any) {
	if strings.TrimSpace(debugDir) == "" {
		return
	}
	if err := os.MkdirAll(debugDir, 0o755); err != nil {
		return
	}
	filename := fmt.Sprintf("ollama-%s-%s.json", time.Now().UTC().Format("20060102T150405.000000000Z"), stage)
	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(debugDir, filename), append(content, '\n'), 0o644)
	pruneOllamaDebugLogs(debugDir, 20)
}

func pruneOllamaDebugLogs(debugDir string, keep int) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(debugDir)
	if err != nil {
		return
	}
	type logFile struct {
		path    string
		modTime time.Time
	}
	files := make([]logFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "ollama-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{
			path:    filepath.Join(debugDir, name),
			modTime: info.ModTime(),
		})
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})
	for _, stale := range files[keep:] {
		_ = os.Remove(stale.path)
	}
}
