package controllers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
)

// AI features (HSN search, purchase bill parsing) send a prompt, optionally
// with one image/PDF, to the store's active AI provider and read back text.
// Gemini and Anthropic use their native APIs; OpenAI, OpenRouter and Ollama
// share the OpenAI-compatible /chat/completions API.

const (
	aiProviderGemini     = "gemini"
	aiProviderOpenAI     = "openai"
	aiProviderAnthropic  = "anthropic"
	aiProviderOpenRouter = "openrouter"
	aiProviderOllama     = "ollama"

	aiRequestTimeout = 120 * time.Second
	aiMaxTokens      = 4096
)

type aiProviderInfo struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	DefaultModel   string `json:"default_model"`
	DefaultBaseURL string `json:"default_base_url"`
	// NeedsKey is false for Ollama (self-hosted, no auth by default).
	NeedsKey bool `json:"needs_key"`
	// SupportsPDF reports whether bill parsing can send PDFs (vs images only).
	SupportsPDF bool   `json:"supports_pdf"`
	KeyURL      string `json:"key_url,omitempty"`
}

var aiProviders = []aiProviderInfo{
	{Key: aiProviderGemini, Label: "Google Gemini", DefaultModel: "gemini-2.5-flash",
		DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", NeedsKey: true, SupportsPDF: true,
		KeyURL: "https://aistudio.google.com/app/apikey"},
	{Key: aiProviderOpenAI, Label: "OpenAI (ChatGPT)", DefaultModel: "gpt-4o-mini",
		DefaultBaseURL: "https://api.openai.com/v1", NeedsKey: true, SupportsPDF: true,
		KeyURL: "https://platform.openai.com/api-keys"},
	{Key: aiProviderAnthropic, Label: "Anthropic (Claude)", DefaultModel: "claude-haiku-5-5",
		DefaultBaseURL: "https://api.anthropic.com", NeedsKey: true, SupportsPDF: true,
		KeyURL: "https://console.anthropic.com/settings/keys"},
	{Key: aiProviderOpenRouter, Label: "OpenRouter", DefaultModel: "google/gemini-2.5-flash",
		DefaultBaseURL: "https://openrouter.ai/api/v1", NeedsKey: true, SupportsPDF: true,
		KeyURL: "https://openrouter.ai/settings/keys"},
	{Key: aiProviderOllama, Label: "Ollama (self-hosted)", DefaultModel: "llama3.2-vision",
		DefaultBaseURL: "http://localhost:11434", NeedsKey: false, SupportsPDF: false},
}

func findAIProvider(key string) (aiProviderInfo, bool) {
	for _, p := range aiProviders {
		if p.Key == key {
			return p, true
		}
	}
	return aiProviderInfo{}, false
}

func normalizeAIProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if _, ok := findAIProvider(p); ok {
		return p
	}
	return aiProviderGemini
}

// aiConfig is a resolved provider connection (secrets decrypted).
type aiConfig struct {
	Provider string
	APIKey   string
	Model    string
	BaseURL  string
}

func (c aiConfig) info() aiProviderInfo {
	info, _ := findAIProvider(c.Provider)
	return info
}

func (c aiConfig) label() string { return c.info().Label }

func (c aiConfig) model() string {
	if m := strings.TrimSpace(c.Model); m != "" {
		return m
	}
	return c.info().DefaultModel
}

func (c aiConfig) baseURL() string {
	if u := strings.TrimSpace(c.BaseURL); u != "" {
		return strings.TrimRight(u, "/")
	}
	return c.info().DefaultBaseURL
}

// ready returns why the connection can't be used, or "" when it can.
func (c aiConfig) ready() string {
	if c.info().NeedsKey && c.APIKey == "" {
		return c.label() + " API key is not configured"
	}
	return ""
}

func loadAIProviderConfig(userID uuid.UUID, provider string) (models.AIProviderConfig, bool) {
	var row models.AIProviderConfig
	if err := utils.DB.Where("user_id = ? AND provider = ?", userID, provider).First(&row).Error; err != nil {
		return models.AIProviderConfig{UserID: userID, Provider: provider}, false
	}
	return row, true
}

// resolveAIConfig builds the connection for one provider of a store, falling
// back to the legacy plaintext Business.GeminiAPIKey for Gemini.
func resolveAIConfig(business models.Business, provider string) (aiConfig, error) {
	row, _ := loadAIProviderConfig(business.UserID, provider)
	cfg := aiConfig{Provider: provider, Model: row.Model, BaseURL: row.BaseURL}
	if row.EncryptedAPIKey != "" {
		key, err := utils.Decrypt(row.EncryptedAPIKey)
		if err != nil {
			return cfg, errors.New("failed to decrypt the saved AI API key")
		}
		cfg.APIKey = key
	} else if provider == aiProviderGemini {
		cfg.APIKey = business.GeminiAPIKey
	}
	return cfg, nil
}

// activeAIConfig returns the store's active provider connection, or an error
// message suitable for the user when it isn't usable.
func activeAIConfig(business models.Business) (aiConfig, error) {
	cfg, err := resolveAIConfig(business, normalizeAIProvider(business.AIProvider))
	if err != nil {
		return cfg, err
	}
	if msg := cfg.ready(); msg != "" {
		return cfg, errors.New(msg + ". Ask your super admin to set it in Developer Settings → Integrations → AI.")
	}
	return cfg, nil
}

// aiAttachment is an image or PDF sent with the prompt.
type aiAttachment struct {
	MimeType string
	Filename string
	Data     []byte
}

func (a *aiAttachment) dataURL() string {
	return "data:" + a.MimeType + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
}

func (a *aiAttachment) isPDF() bool { return a.MimeType == "application/pdf" }

var aiHTTPClient = &http.Client{Timeout: aiRequestTimeout}

// aiGenerate sends prompt (and an optional attachment) to the provider and
// returns the model's text reply.
func aiGenerate(ctx context.Context, cfg aiConfig, prompt string, att *aiAttachment) (string, error) {
	if msg := cfg.ready(); msg != "" {
		return "", errors.New(msg)
	}
	if att != nil && att.isPDF() && !cfg.info().SupportsPDF {
		return "", fmt.Errorf("%s can't read PDFs. Upload a photo or screenshot of the bill instead", cfg.label())
	}
	switch cfg.Provider {
	case aiProviderGemini:
		return aiGenerateGemini(ctx, cfg, prompt, att)
	case aiProviderAnthropic:
		return aiGenerateAnthropic(ctx, cfg, prompt, att)
	case aiProviderOpenAI, aiProviderOpenRouter, aiProviderOllama:
		return aiGenerateOpenAICompatible(ctx, cfg, prompt, att)
	}
	return "", fmt.Errorf("unsupported AI provider %q", cfg.Provider)
}

// aiPostJSON posts body and decodes a 2xx JSON reply into out. Error bodies
// are included (truncated) so the user can see e.g. "model not found".
func aiPostJSON(ctx context.Context, cfg aiConfig, endpoint string, headers map[string]string, body, out interface{}) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %v", cfg.label(), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s API error (%d): %s", cfg.label(), resp.StatusCode, truncateWALog(strings.TrimSpace(string(raw)), 500))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("failed to read %s response: %v", cfg.label(), err)
	}
	return nil
}

func aiGenerateGemini(ctx context.Context, cfg aiConfig, prompt string, att *aiAttachment) (string, error) {
	parts := []map[string]interface{}{{"text": prompt}}
	if att != nil {
		parts = append(parts, map[string]interface{}{
			"inline_data": map[string]interface{}{
				"mime_type": att.MimeType,
				"data":      base64.StdEncoding.EncodeToString(att.Data),
			},
		})
	}
	body := map[string]interface{}{"contents": []map[string]interface{}{{"parts": parts}}}
	endpoint := cfg.baseURL() + "/models/" + url.PathEscape(cfg.model()) + ":generateContent"
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	// Key in a header rather than ?key= so it never lands in proxy logs.
	if err := aiPostJSON(ctx, cfg, endpoint, map[string]string{"x-goog-api-key": cfg.APIKey}, body, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	if len(out.Candidates) > 0 {
		for _, p := range out.Candidates[0].Content.Parts {
			sb.WriteString(p.Text)
		}
	}
	return aiNonEmpty(cfg, sb.String())
}

func aiGenerateAnthropic(ctx context.Context, cfg aiConfig, prompt string, att *aiAttachment) (string, error) {
	var content []map[string]interface{}
	if att != nil {
		blockType := "image"
		if att.isPDF() {
			blockType = "document"
		}
		content = append(content, map[string]interface{}{
			"type": blockType,
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": att.MimeType,
				"data":       base64.StdEncoding.EncodeToString(att.Data),
			},
		})
	}
	content = append(content, map[string]interface{}{"type": "text", "text": prompt})
	body := map[string]interface{}{
		"model":      cfg.model(),
		"max_tokens": aiMaxTokens,
		"messages":   []map[string]interface{}{{"role": "user", "content": content}},
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	headers := map[string]string{"x-api-key": cfg.APIKey, "anthropic-version": "2023-06-01"}
	if err := aiPostJSON(ctx, cfg, cfg.baseURL()+"/v1/messages", headers, body, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return aiNonEmpty(cfg, sb.String())
}

func aiGenerateOpenAICompatible(ctx context.Context, cfg aiConfig, prompt string, att *aiAttachment) (string, error) {
	var content interface{} = prompt
	if att != nil {
		parts := []map[string]interface{}{{"type": "text", "text": prompt}}
		if att.isPDF() {
			name := att.Filename
			if name == "" {
				name = "document.pdf"
			}
			parts = append(parts, map[string]interface{}{
				"type": "file",
				"file": map[string]interface{}{"filename": name, "file_data": att.dataURL()},
			})
		} else {
			parts = append(parts, map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": att.dataURL()},
			})
		}
		content = parts
	}
	body := map[string]interface{}{
		"model":    cfg.model(),
		"messages": []map[string]interface{}{{"role": "user", "content": content}},
	}

	base := cfg.baseURL()
	// Ollama serves the OpenAI-compatible API under /v1; accept the plain
	// server URL (http://host:11434) people usually copy.
	if cfg.Provider == aiProviderOllama && !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	headers := map[string]string{}
	if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}
	if cfg.Provider == aiProviderOpenRouter {
		headers["X-Title"] = "TruERP"
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := aiPostJSON(ctx, cfg, base+"/chat/completions", headers, body, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return aiNonEmpty(cfg, "")
	}
	return aiNonEmpty(cfg, out.Choices[0].Message.Content)
}

func aiNonEmpty(cfg aiConfig, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("no response from %s", cfg.label())
	}
	return text, nil
}

// validateAIBaseURL accepts "" (use default) or a full http(s) URL.
func validateAIBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("API base URL must be a full http(s) URL, e.g. http://192.168.1.10:11434")
	}
	return raw, nil
}

// aiModelInfo is one selectable model. Vision is nil when the provider
// doesn't say whether the model reads images.
type aiModelInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Vision *bool  `json:"vision,omitempty"`
}

// aiGetJSON GETs endpoint and decodes a 2xx JSON reply into out.
func aiGetJSON(ctx context.Context, cfg aiConfig, endpoint string, headers map[string]string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %v", cfg.label(), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s API error (%d): %s", cfg.label(), resp.StatusCode, truncateWALog(strings.TrimSpace(string(raw)), 500))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("failed to read %s model list: %v", cfg.label(), err)
	}
	return nil
}

// openAINonChatModel filters OpenAI's /models list (which also returns
// embedding, speech, image and moderation models) down to chat models.
func openAINonChatModel(id string) bool {
	id = strings.ToLower(id)
	for _, frag := range []string{"embedding", "whisper", "tts", "dall-e", "moderation", "davinci", "babbage",
		"transcribe", "realtime", "audio", "image", "search", "computer-use", "codex"} {
		if strings.Contains(id, frag) {
			return true
		}
	}
	return false
}

// aiListModels asks the provider which models this connection can use.
func aiListModels(ctx context.Context, cfg aiConfig) ([]aiModelInfo, error) {
	// OpenRouter's catalogue and a local Ollama need no key.
	if cfg.Provider != aiProviderOpenRouter {
		if msg := cfg.ready(); msg != "" {
			return nil, errors.New(msg)
		}
	}
	var models []aiModelInfo
	switch cfg.Provider {
	case aiProviderGemini:
		var out struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		if err := aiGetJSON(ctx, cfg, cfg.baseURL()+"/models?pageSize=1000", map[string]string{"x-goog-api-key": cfg.APIKey}, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Models {
			canGenerate := false
			for _, method := range m.SupportedGenerationMethods {
				if method == "generateContent" {
					canGenerate = true
				}
			}
			if !canGenerate {
				continue
			}
			models = append(models, aiModelInfo{ID: strings.TrimPrefix(m.Name, "models/"), Name: m.DisplayName})
		}

	case aiProviderAnthropic:
		var out struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
		}
		headers := map[string]string{"x-api-key": cfg.APIKey, "anthropic-version": "2023-06-01"}
		if err := aiGetJSON(ctx, cfg, cfg.baseURL()+"/v1/models?limit=1000", headers, &out); err != nil {
			return nil, err
		}
		vision := true // every current Claude model reads images and PDFs
		for _, m := range out.Data {
			models = append(models, aiModelInfo{ID: m.ID, Name: m.DisplayName, Vision: &vision})
		}

	case aiProviderOpenAI:
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := aiGetJSON(ctx, cfg, cfg.baseURL()+"/models", map[string]string{"Authorization": "Bearer " + cfg.APIKey}, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Data {
			if !openAINonChatModel(m.ID) {
				models = append(models, aiModelInfo{ID: m.ID})
			}
		}

	case aiProviderOpenRouter:
		var out struct {
			Data []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				Architecture struct {
					InputModalities []string `json:"input_modalities"`
				} `json:"architecture"`
			} `json:"data"`
		}
		headers := map[string]string{}
		if cfg.APIKey != "" {
			headers["Authorization"] = "Bearer " + cfg.APIKey
		}
		if err := aiGetJSON(ctx, cfg, cfg.baseURL()+"/models", headers, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Data {
			vision := false
			for _, mod := range m.Architecture.InputModalities {
				if mod == "image" {
					vision = true
				}
			}
			models = append(models, aiModelInfo{ID: m.ID, Name: m.Name, Vision: &vision})
		}

	case aiProviderOllama:
		// Installed models come from Ollama's native API, not the /v1 one.
		base := strings.TrimSuffix(cfg.baseURL(), "/v1")
		var out struct {
			Models []struct {
				Name    string `json:"name"`
				Details struct {
					Families []string `json:"families"`
				} `json:"details"`
			} `json:"models"`
		}
		if err := aiGetJSON(ctx, cfg, base+"/api/tags", nil, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Models {
			var vision *bool
			for _, fam := range m.Details.Families {
				if fam == "clip" || fam == "mllama" {
					v := true
					vision = &v
				}
			}
			models = append(models, aiModelInfo{ID: m.Name, Vision: vision})
		}

	default:
		return nil, fmt.Errorf("unsupported AI provider %q", cfg.Provider)
	}

	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
