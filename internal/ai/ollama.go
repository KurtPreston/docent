package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// RunMode sends the mode's resolved instruction and the formatted activity
// body to Ollama and returns the model's Markdown response. The activity
// formatter is depth-adjusted for daily-plan / custom-prompt to keep model
// section headings from colliding with repo headings.
func (p OllamaProvider) RunMode(ctx context.Context, in RunInput) (string, error) {
	formatter := p.formatterOrDefault()
	if needsNested(in.ModeID) {
		formatter = NestRepoChronologicalDepth(formatter)
	}
	payload, err := BuildPrompt(in.Instruction, in, formatter)
	if err != nil {
		return "", err
	}
	p.printPrompt(in.StreamOut, payload)
	raw, err := p.chatMarkdown(ctx, payload, in.DebugDir, in.StreamOut, debugStageFor(in.ModeID))
	if err != nil {
		return "", err
	}
	return StripMarkdownFence(raw), nil
}

// needsNested returns true when the mode's outer rendering wraps the
// activity in a `##` section (so repo headings should drop to `###`).
func needsNested(modeID string) bool {
	switch modeID {
	case "daily-plan", "custom-prompt":
		return true
	default:
		return false
	}
}

func debugStageFor(modeID string) string {
	id := strings.TrimSpace(modeID)
	if id == "" {
		id = "request"
	}
	return id + "-request"
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
	writeAIDebugLog(debugDir, "ollama", requestLogStage, map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"base_url":  p.BaseURL,
		"model":     p.Model,
		"request":   json.RawMessage(body),
		"prompt":    userContent,
	})
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
		writeAIDebugLog(debugDir, "ollama", "error", map[string]any{
			"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
			"status":      res.Status,
			"status_code": res.StatusCode,
			"response":    strings.TrimSpace(errBody.String()),
		})
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
			writeAIDebugLog(debugDir, "ollama", "error", map[string]any{
				"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
				"error":     fmt.Sprintf("ollama stream parse: %v", err),
			})
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
	writeAIDebugLog(debugDir, "ollama", "markdown-response", map[string]any{
		"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
		"status":          res.Status,
		"status_code":     res.StatusCode,
		"response_raw":    streamChunks,
		"message_content": out,
	})
	return out, nil
}
