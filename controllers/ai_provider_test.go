package controllers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAIServer records the request and replies with body.
func fakeAIServer(t *testing.T, body string) (*httptest.Server, *http.Request, *map[string]interface{}) {
	t.Helper()
	var gotReq http.Request
	got := map[string]interface{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotReq, &got
}

func TestAIGenerateGemini(t *testing.T) {
	srv, req, body := fakeAIServer(t, `{"candidates":[{"content":{"parts":[{"text":"{\"ok\":1}"}]}}]}`)
	cfg := aiConfig{Provider: aiProviderGemini, APIKey: "g-key", BaseURL: srv.URL}
	text, err := aiGenerate(context.Background(), cfg, "hi", &aiAttachment{MimeType: "application/pdf", Data: []byte("%PDF")})
	if err != nil || text != `{"ok":1}` {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if req.URL.Path != "/models/gemini-2.5-flash:generateContent" || req.Header.Get("x-goog-api-key") != "g-key" || req.URL.RawQuery != "" {
		t.Fatalf("request = %s ?%s key=%q", req.URL.Path, req.URL.RawQuery, req.Header.Get("x-goog-api-key"))
	}
	parts := (*body)["contents"].([]interface{})[0].(map[string]interface{})["parts"].([]interface{})
	if len(parts) != 2 {
		t.Fatalf("parts = %v", parts)
	}
}

func TestAIGenerateAnthropic(t *testing.T) {
	srv, req, body := fakeAIServer(t, `{"content":[{"type":"text","text":"OK"}]}`)
	cfg := aiConfig{Provider: aiProviderAnthropic, APIKey: "a-key", BaseURL: srv.URL, Model: "claude-sonnet-5-5"}
	text, err := aiGenerate(context.Background(), cfg, "hi", &aiAttachment{MimeType: "application/pdf", Data: []byte("%PDF")})
	if err != nil || text != "OK" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if req.URL.Path != "/v1/messages" || req.Header.Get("x-api-key") != "a-key" || req.Header.Get("anthropic-version") == "" {
		t.Fatalf("request = %s headers=%v", req.URL.Path, req.Header)
	}
	if (*body)["model"] != "claude-sonnet-5-5" {
		t.Fatalf("model = %v", (*body)["model"])
	}
	content := (*body)["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})
	if content[0].(map[string]interface{})["type"] != "document" {
		t.Fatalf("PDF should be a document block: %v", content[0])
	}
}

func TestAIGenerateOpenAICompatible(t *testing.T) {
	reply := `{"choices":[{"message":{"content":"OK"}}]}`
	for _, tc := range []struct {
		provider, key, wantPath, wantAuth string
	}{
		{aiProviderOpenAI, "sk-1", "/chat/completions", "Bearer sk-1"},
		{aiProviderOpenRouter, "or-1", "/chat/completions", "Bearer or-1"},
		{aiProviderOllama, "", "/v1/chat/completions", ""},
	} {
		srv, req, body := fakeAIServer(t, reply)
		cfg := aiConfig{Provider: tc.provider, APIKey: tc.key, BaseURL: srv.URL}
		text, err := aiGenerate(context.Background(), cfg, "hi", &aiAttachment{MimeType: "image/png", Data: []byte{1}})
		if err != nil || text != "OK" {
			t.Fatalf("%s: text=%q err=%v", tc.provider, text, err)
		}
		if req.URL.Path != tc.wantPath || req.Header.Get("Authorization") != tc.wantAuth {
			t.Fatalf("%s: path=%s auth=%q", tc.provider, req.URL.Path, req.Header.Get("Authorization"))
		}
		parts := (*body)["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})
		img := parts[1].(map[string]interface{})["image_url"].(map[string]interface{})["url"].(string)
		if !strings.HasPrefix(img, "data:image/png;base64,") {
			t.Fatalf("%s: image part = %v", tc.provider, parts[1])
		}
	}
}

func TestAIGenerateErrors(t *testing.T) {
	if _, err := aiGenerate(context.Background(), aiConfig{Provider: aiProviderOpenAI}, "hi", nil); err == nil {
		t.Fatal("missing key accepted")
	}
	_, err := aiGenerate(context.Background(), aiConfig{Provider: aiProviderOllama}, "hi",
		&aiAttachment{MimeType: "application/pdf", Data: []byte("%PDF")})
	if err == nil || !strings.Contains(err.Error(), "PDF") {
		t.Fatalf("ollama PDF err = %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()
	_, err = aiGenerate(context.Background(), aiConfig{Provider: aiProviderOpenAI, APIKey: "k", BaseURL: srv.URL}, "hi", nil)
	if err == nil || !strings.Contains(err.Error(), "model not found") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("API error = %v", err)
	}
}

func TestAIListModels(t *testing.T) {
	cases := []struct {
		provider, key, reply, wantPath string
		want                           []string
	}{
		{aiProviderGemini, "g", `{"models":[{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","supportedGenerationMethods":["generateContent"]},{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]}]}`,
			"/models", []string{"gemini-2.5-flash"}},
		{aiProviderOpenAI, "sk", `{"data":[{"id":"gpt-4o"},{"id":"text-embedding-3-small"},{"id":"whisper-1"},{"id":"gpt-4o-mini"}]}`,
			"/models", []string{"gpt-4o", "gpt-4o-mini"}},
		{aiProviderAnthropic, "a", `{"data":[{"id":"claude-sonnet-5-5","display_name":"Claude Sonnet 5.5"}]}`,
			"/v1/models", []string{"claude-sonnet-5-5"}},
		{aiProviderOpenRouter, "", `{"data":[{"id":"openai/gpt-4o","name":"GPT-4o","architecture":{"input_modalities":["text","image"]}}]}`,
			"/models", []string{"openai/gpt-4o"}},
		{aiProviderOllama, "", `{"models":[{"name":"llama3.2-vision:latest","details":{"families":["mllama"]}}]}`,
			"/api/tags", []string{"llama3.2-vision:latest"}},
	}
	for _, tc := range cases {
		srv, req, _ := fakeAIServer(t, tc.reply)
		base := srv.URL
		if tc.provider == aiProviderOllama {
			base += "/v1" // the native /api/tags must still be used
		}
		models, err := aiListModels(context.Background(), aiConfig{Provider: tc.provider, APIKey: tc.key, BaseURL: base})
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if req.Method != http.MethodGet || req.URL.Path != tc.wantPath {
			t.Fatalf("%s: %s %s", tc.provider, req.Method, req.URL.Path)
		}
		var got []string
		for _, m := range models {
			got = append(got, m.ID)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("%s: models = %v, want %v", tc.provider, got, tc.want)
		}
		if tc.provider == aiProviderOpenRouter && (models[0].Vision == nil || !*models[0].Vision) {
			t.Fatalf("openrouter vision flag missing: %+v", models[0])
		}
	}
	if _, err := aiListModels(context.Background(), aiConfig{Provider: aiProviderOpenAI}); err == nil {
		t.Fatal("listing OpenAI models without a key should fail")
	}
}
