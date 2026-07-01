package wmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Window describes an open Cursor window reported by docent-wm.
type Window struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	App   string `json:"app"`
	Host  string `json:"host,omitempty"`
}

// WindowsResponse is returned by GET /windows.
type WindowsResponse struct {
	Windows []Window `json:"windows"`
}

// OpenRequest is the body for POST /open.
type OpenRequest struct {
	Host string `json:"host"`
	Path string `json:"path"`
	Name string `json:"name"`
	URI  string `json:"uri,omitempty"`
}

// FocusRequest is the body for POST /focus.
type FocusRequest struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Client talks to a docent-wm REST service.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) ListWindows(ctx context.Context) ([]Window, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/windows", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docent-wm GET /windows: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out WindowsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Windows, nil
}

func (c *Client) Open(ctx context.Context, body OpenRequest) error {
	return c.post(ctx, "/open", body)
}

func (c *Client) Focus(ctx context.Context, body FocusRequest) error {
	return c.post(ctx, "/focus", body)
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("docent-wm POST %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// ParseCursorTitle extracts workspace leaf and optional SSH host from a Cursor title.
func ParseCursorTitle(title string) (leaf, host string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", ""
	}
	const marker = "[SSH:"
	if idx := strings.Index(title, marker); idx >= 0 {
		end := strings.Index(title[idx:], "]")
		if end > 0 {
			inner := title[idx+len(marker) : idx+end]
			host = strings.TrimSpace(inner)
			pre := strings.TrimSpace(title[:idx])
			parts := strings.Split(pre, " - ")
			if len(parts) > 0 {
				leaf = strings.TrimSpace(parts[len(parts)-1])
			}
			return leaf, host
		}
	}
	core := title
	if strings.HasSuffix(core, " - Cursor") {
		core = strings.TrimSuffix(core, " - Cursor")
	}
	parts := strings.Split(core, " - ")
	leaf = strings.TrimSpace(parts[len(parts)-1])
	if leaf == "Cursor" {
		leaf = ""
	}
	return leaf, host
}
