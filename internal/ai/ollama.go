package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
		return PlanningOutput{}, fmt.Errorf("ollama %s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	var parsed ollamaChatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return PlanningOutput{}, fmt.Errorf("ollama response: %w", err)
	}
	return ParsePlanningOutput([]byte(parsed.Message.Content))
}
