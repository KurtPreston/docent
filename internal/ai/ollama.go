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

type ollamaChatResponse struct {
	Message ollamaMsg `json:"message"`
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

func (p OllamaProvider) ProposeDayPlan(ctx context.Context, input PlanningInput) (PlanningOutput, error) {
	return p.run(ctx, "Create a concise daily plan as JSON matching the requested schema.", input)
}

func (p OllamaProvider) ReflectEndOfDay(ctx context.Context, input PlanningInput) (PlanningOutput, error) {
	return p.run(ctx, "Create a concise end-of-day reflection as JSON matching the requested schema.", input)
}

func (p OllamaProvider) run(ctx context.Context, instruction string, input PlanningInput) (PlanningOutput, error) {
	payload, err := BuildPrompt(instruction, input)
	if err != nil {
		return PlanningOutput{}, err
	}
	body, err := json.Marshal(ollamaChatRequest{
		Model: p.Model,
		Messages: []ollamaMsg{
			{Role: "user", Content: payload},
		},
		Stream: true,
	})
	if err != nil {
		return PlanningOutput{}, err
	}
	if input.DebugDir != "" {
		writeOllamaDebugLog(input.DebugDir, "request", map[string]any{
			"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
			"base_url":    p.BaseURL,
			"model":       p.Model,
			"instruction": instruction,
			"request":     json.RawMessage(body),
			"prompt":      payload,
		})
	}
	url := strings.TrimRight(p.BaseURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return PlanningOutput{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client().Do(req)
	if err != nil {
		return PlanningOutput{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(res.Body)
		if input.DebugDir != "" {
			writeOllamaDebugLog(input.DebugDir, "error", map[string]any{
				"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
				"status":      res.Status,
				"status_code": res.StatusCode,
				"response":    strings.TrimSpace(errBody.String()),
			})
		}
		return PlanningOutput{}, fmt.Errorf("ollama %s: %s", res.Status, strings.TrimSpace(errBody.String()))
	}
	decoder := json.NewDecoder(res.Body)
	var content strings.Builder
	streamChunks := make([]ollamaChatStreamResponse, 0, 128)
	for {
		var chunk ollamaChatStreamResponse
		if err := decoder.Decode(&chunk); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return PlanningOutput{}, err
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if input.DebugDir != "" {
				writeOllamaDebugLog(input.DebugDir, "error", map[string]any{
					"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
					"error":     fmt.Sprintf("ollama stream parse: %v", err),
				})
			}
			return PlanningOutput{}, fmt.Errorf("ollama stream: %w", err)
		}
		streamChunks = append(streamChunks, chunk)
		// Qwen3 and similar may stream reasoning in `thinking` while `content` stays empty
		// until the final answer; Ollama chat deltas are in both fields.
		if chunk.Message.Thinking != "" {
			if input.OllamaStreamOut != nil {
				_, _ = io.WriteString(input.OllamaStreamOut, chunk.Message.Thinking)
			}
		}
		if chunk.Message.Content != "" {
			content.WriteString(chunk.Message.Content)
			if input.OllamaStreamOut != nil {
				_, _ = io.WriteString(input.OllamaStreamOut, chunk.Message.Content)
			}
		}
		if chunk.Done {
			break
		}
	}
	parsed := ollamaChatResponse{
		Message: ollamaMsg{Content: content.String()},
	}
	if input.DebugDir != "" {
		writeOllamaDebugLog(input.DebugDir, "response", map[string]any{
			"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
			"status":          res.Status,
			"status_code":     res.StatusCode,
			"response_raw":    streamChunks,
			"message_content": parsed.Message.Content,
		})
	}
	return ParsePlanningOutput([]byte(parsed.Message.Content))
}

// ClassifyTaskSignals uses a single non-streaming JSON response for reliable parsing.
func (p OllamaProvider) ClassifyTaskSignals(ctx context.Context, in TaskSignalsInput) (TaskSignalsOutput, error) {
	instruction := "For each open signal, choose an action: ignore (noise), assign_task (map to an existing task_id from the input), propose_task (suggest a new task for the user to confirm later), or pending (uncertain). Prefer assign_task when a task link or title clearly matches. Never invent task_ids that are not in tasks. Stay within the JSON schema."
	payload, err := BuildTaskSignalsPrompt(instruction, in)
	if err != nil {
		return TaskSignalsOutput{}, err
	}
	body, err := json.Marshal(ollamaChatRequest{
		Model: p.Model,
		Messages: []ollamaMsg{
			{Role: "user", Content: payload},
		},
		Stream: false,
		Format: "json",
	})
	if err != nil {
		return TaskSignalsOutput{}, err
	}
	if in.DebugDir != "" {
		writeOllamaDebugLog(in.DebugDir, "task-signals-request", map[string]any{
			"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
			"base_url":    p.BaseURL,
			"model":       p.Model,
			"instruction": instruction,
			"request":     json.RawMessage(body),
			"prompt":      payload,
		})
	}
	url := strings.TrimRight(p.BaseURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return TaskSignalsOutput{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client().Do(req)
	if err != nil {
		return TaskSignalsOutput{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(res.Body)
		if in.DebugDir != "" {
			writeOllamaDebugLog(in.DebugDir, "task-signals-error", map[string]any{
				"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
				"status":      res.Status,
				"status_code": res.StatusCode,
				"response":    strings.TrimSpace(errBody.String()),
			})
		}
		return TaskSignalsOutput{}, fmt.Errorf("ollama %s: %s", res.Status, strings.TrimSpace(errBody.String()))
	}
	var parsed ollamaChatResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return TaskSignalsOutput{}, err
	}
	if in.DebugDir != "" {
		writeOllamaDebugLog(in.DebugDir, "task-signals-response", map[string]any{
			"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
			"message_content": parsed.Message.Content,
		})
	}
	return ParseTaskSignalsOutput([]byte(parsed.Message.Content))
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
