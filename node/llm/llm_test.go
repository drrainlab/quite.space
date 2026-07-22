package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k-123" {
			t.Fatalf("missing bearer")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "hello"}}},
		})
	}))
	defer srv.Close()
	c := New()
	out, err := c.Generate(context.Background(),
		Config{Provider: OpenAICompatible, Model: "m", BaseURL: srv.URL, APIKey: "k-123"},
		"sys", "user")
	if err != nil || out != "hello" {
		t.Fatalf("openai adapter: %q %v", out, err)
	}
}

func TestAnthropicAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-a" || r.Header.Get("anthropic-version") == "" {
			t.Fatal("bad anthropic headers")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "calm violet"}},
		})
	}))
	defer srv.Close()
	c := New()
	out, err := c.Generate(context.Background(),
		Config{Provider: Anthropic, Model: "claude", BaseURL: srv.URL, APIKey: "sk-a"}, "sys", "u")
	if err != nil || out != "calm violet" {
		t.Fatalf("anthropic adapter: %q %v", out, err)
	}
}

func TestLocalOllamaAdapterNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": "local ok"}})
	}))
	defer srv.Close()
	c := New()
	out, err := c.Generate(context.Background(),
		Config{Provider: Local, Model: "llama", BaseURL: srv.URL}, "sys", "u")
	if err != nil || out != "local ok" {
		t.Fatalf("local adapter: %q %v", out, err)
	}
}

func TestProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()
	c := New()
	_, err := c.Generate(context.Background(),
		Config{Provider: OpenAICompatible, Model: "m", BaseURL: srv.URL, APIKey: "x"}, "s", "u")
	if err == nil {
		t.Fatal("expected provider error")
	}
}
