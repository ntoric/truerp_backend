package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"truerp/models"

	"github.com/google/uuid"
)

const telegramAPIBase = "https://api.telegram.org"

// TelegramConfig holds the credentials needed to send Telegram Bot API
// requests for a given user (store owner).
type TelegramConfig struct {
	BotToken string
}

// GetTelegramConfig loads the Telegram bot token from environment variables.
// Used as a global fallback when a user has not configured their own bot.
func GetTelegramConfig() TelegramConfig {
	return TelegramConfig{
		BotToken: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
	}
}

// GetTelegramConfigForUser loads the Telegram bot token from the user's
// DeveloperSettings record. The stored token is decrypted before being
// returned.
func GetTelegramConfigForUser(userID uuid.UUID) (TelegramConfig, error) {
	var settings models.DeveloperSettings
	if err := DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		return TelegramConfig{}, fmt.Errorf("developer settings not found for user: %w", err)
	}

	token := ""
	if settings.EncryptedTelegramBotToken != "" {
		decrypted, err := Decrypt(settings.EncryptedTelegramBotToken)
		if err != nil {
			return TelegramConfig{}, fmt.Errorf("failed to decrypt Telegram bot token: %w", err)
		}
		token = decrypted
	}

	// Fall back to the global env token if the user has not configured one.
	if token == "" {
		token = strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	}

	return TelegramConfig{BotToken: token}, nil
}

// TelegramConfiguredForUser reports whether a Telegram bot token is available
// for the given user (either in their DeveloperSettings or as a global env
// fallback).
func TelegramConfiguredForUser(userID uuid.UUID) bool {
	cfg, err := GetTelegramConfigForUser(userID)
	if err != nil {
		return false
	}
	return cfg.BotToken != ""
}

// telegramAPIURL builds the Bot API URL for the given method.
func telegramAPIURL(token, method string) string {
	return fmt.Sprintf("%s/bot%s/%s", telegramAPIBase, token, method)
}

// httpClient is a small shared client with a sane timeout for Bot API calls.
var telegramHTTPClient = &http.Client{Timeout: 30 * time.Second}

type telegramAPIError struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

func (e telegramAPIError) Error() string {
	return fmt.Sprintf("telegram api error %d: %s", e.ErrorCode, e.Description)
}

// parseTelegramAPIError reads the response body and returns a typed error if
// the Bot API responded with ok=false.
func parseTelegramAPIError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram: failed to read response: %w", err)
	}
	var apiErr telegramAPIError
	if jsonErr := json.Unmarshal(body, &apiErr); jsonErr == nil && !apiErr.OK {
		if apiErr.Description == "" {
			apiErr.Description = string(body)
		}
		return apiErr
	}
	return fmt.Errorf("telegram: unexpected response (status %d): %s", resp.StatusCode, string(body))
}

// SendTelegramMessage sends a plain-text message to the given chat via the
// Bot API sendMessage method. chatTarget is a numeric chat ID or
// @channelusername.
func SendTelegramMessage(token, chatTarget, text string) error {
	if token == "" {
		return fmt.Errorf("telegram bot token is not configured")
	}
	if strings.TrimSpace(chatTarget) == "" {
		return fmt.Errorf("telegram chat target is empty")
	}

	payload := map[string]string{
		"chat_id": chatTarget,
		"text":    text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, telegramAPIURL(token, "sendMessage"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return parseTelegramAPIError(resp)
	}
	// Drain & discard the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// SendTelegramDocument uploads a file as a Telegram document (sendDocument)
// with an optional caption. pdfBytes is the raw file content; filename is the
// name shown in Telegram.
func SendTelegramDocument(token, chatTarget, filename string, fileBytes []byte, caption string) error {
	if token == "" {
		return fmt.Errorf("telegram bot token is not configured")
	}
	if strings.TrimSpace(chatTarget) == "" {
		return fmt.Errorf("telegram chat target is empty")
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	if err := writer.WriteField("chat_id", chatTarget); err != nil {
		return fmt.Errorf("telegram: write chat_id field: %w", err)
	}
	if caption != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return fmt.Errorf("telegram: write caption field: %w", err)
		}
	}

	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return fmt.Errorf("telegram: create form file: %w", err)
	}
	if _, err := part.Write(fileBytes); err != nil {
		return fmt.Errorf("telegram: write file bytes: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("telegram: close multipart writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, telegramAPIURL(token, "sendDocument"), &buf)
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return parseTelegramAPIError(resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// TestTelegramBot validates a bot token by calling the getMe method.
// Returns the bot's username on success.
func TestTelegramBot(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("telegram bot token is required")
	}

	req, err := http.NewRequest(http.MethodGet, telegramAPIURL(token, "getMe"), nil)
	if err != nil {
		return "", fmt.Errorf("telegram: build request: %w", err)
	}

	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("telegram: send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("telegram: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr telegramAPIError
		if jsonErr := json.Unmarshal(body, &apiErr); jsonErr == nil && !apiErr.OK {
			if apiErr.Description == "" {
				apiErr.Description = string(body)
			}
			return "", apiErr
		}
		return "", fmt.Errorf("telegram: unexpected response (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("telegram: parse response: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("telegram: getMe returned ok=false")
	}
	return result.Result.Username, nil
}
