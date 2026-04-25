package ai

import (
	"bytes"
	"context"
	"encoding/json"
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
	Model    string       `json:"model"`
	Messages []ollamaMsg  `json:"messages"`
	Stream   bool         `json:"stream"`
	Format   string       `json:"format,omitempty"`
}

type ollamaMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message ollamaMsg `json:"message"`
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
		Stream: false,
	})
	if err != nil {
		return PlanningOutput{}, err
	}
	if input.DebugDir != "" {
		writeOllamaDebugLog(input.DebugDir, "request", map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"base_url":  p.BaseURL,
			"model":     p.Model,
			"instruction": instruction,
			"request":   json.RawMessage(body),
			"prompt":    payload,
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
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return PlanningOutput{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if input.DebugDir != "" {
			writeOllamaDebugLog(input.DebugDir, "error", map[string]any{
				"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
				"status":      res.Status,
				"status_code": res.StatusCode,
				"response":    strings.TrimSpace(string(raw)),
			})
		}
		return PlanningOutput{}, fmt.Errorf("ollama %s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	var parsed ollamaChatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		if input.DebugDir != "" {
			writeOllamaDebugLog(input.DebugDir, "error", map[string]any{
				"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
				"error":     fmt.Sprintf("ollama response parse: %v", err),
				"response":  strings.TrimSpace(string(raw)),
			})
		}
		return PlanningOutput{}, fmt.Errorf("ollama response: %w", err)
	}
	if input.DebugDir != "" {
		writeOllamaDebugLog(input.DebugDir, "response", map[string]any{
			"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
			"status":          res.Status,
			"status_code":     res.StatusCode,
			"response_raw":    json.RawMessage(raw),
			"message_content": parsed.Message.Content,
		})
	}
	return ParsePlanningOutput([]byte(parsed.Message.Content))
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
