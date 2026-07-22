// Package llm is a small, provider-agnostic client for text generation
// (PUI-2). It runs node-side so API keys stay in the encrypted keystore and
// never reach the browser. It speaks to the user-chosen provider only, on an
// explicit action, and never logs the key.
package llm

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

// Providers.
const (
	Anthropic        = "anthropic"
	OpenAI           = "openai"
	OpenAICompatible = "openai-compatible"
	Local            = "local" // Ollama /api/chat (LM Studio → use openai-compatible)
)

// Config is the user's LLM settings. APIKey is empty for Local.
type Config struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
}

// Client performs generation. HTTP is injectable for tests.
type Client struct{ HTTP *http.Client }

// New returns a client with a sane timeout.
func New() *Client { return &Client{HTTP: &http.Client{Timeout: 60 * time.Second}} }

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// Generate sends a system + user prompt and returns the model's text.
func (c *Client) Generate(ctx context.Context, cfg Config, system, user string) (string, error) {
	if cfg.Model == "" {
		return "", errors.New("llm: no model configured")
	}
	switch cfg.Provider {
	case Anthropic:
		return c.anthropic(ctx, cfg, system, user)
	case OpenAI, OpenAICompatible:
		return c.openai(ctx, cfg, system, user)
	case Local:
		return c.ollama(ctx, cfg, system, user)
	default:
		return "", fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}

func (c *Client) do(ctx context.Context, method, url string, headers map[string]string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		// Surface the provider's status/message but never echo the request key.
		return nil, fmt.Errorf("llm: provider returned %d: %s", resp.StatusCode, snippet(data))
	}
	return data, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func base(cfg Config, fallback string) string {
	u := strings.TrimRight(cfg.BaseURL, "/")
	if u == "" {
		u = fallback
	}
	return u
}

// ---- Anthropic Messages API ----
func (c *Client) anthropic(ctx context.Context, cfg Config, system, user string) (string, error) {
	if cfg.APIKey == "" {
		return "", errors.New("llm: anthropic requires an API key")
	}
	url := base(cfg, "https://api.anthropic.com") + "/v1/messages"
	body := map[string]any{
		"model": cfg.Model, "max_tokens": 1024, "system": system,
		"messages": []map[string]any{{"role": "user", "content": user}},
	}
	data, err := c.do(ctx, "POST", url, map[string]string{
		"x-api-key": cfg.APIKey, "anthropic-version": "2023-06-01",
	}, body)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, p := range out.Content {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String(), nil
}

// ---- OpenAI / OpenAI-compatible chat completions ----
func (c *Client) openai(ctx context.Context, cfg Config, system, user string) (string, error) {
	fallback := "https://api.openai.com"
	if cfg.Provider == OpenAICompatible {
		if cfg.BaseURL == "" {
			return "", errors.New("llm: openai-compatible requires a base URL")
		}
		fallback = ""
	}
	url := base(cfg, fallback) + "/v1/chat/completions"
	headers := map[string]string{}
	if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}
	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	data, err := c.do(ctx, "POST", url, headers, body)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("llm: empty completion")
	}
	return out.Choices[0].Message.Content, nil
}

// ---- Local (Ollama /api/chat) ----
func (c *Client) ollama(ctx context.Context, cfg Config, system, user string) (string, error) {
	url := base(cfg, "http://localhost:11434") + "/api/chat"
	body := map[string]any{
		"model": cfg.Model, "stream": false,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	data, err := c.do(ctx, "POST", url, nil, body)
	if err != nil {
		return "", err
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	return out.Message.Content, nil
}
