package models

import (
	"time"

	"github.com/google/uuid"
)

// AIProviderConfig holds one AI provider's connection for a store (store-owner
// user_id scope): Gemini, OpenAI (ChatGPT), Anthropic (Claude), OpenRouter or
// Ollama. A store keeps one row per provider it has set up, so switching the
// active provider (Business.AIProvider) never loses another provider's key.
type AIProviderConfig struct {
	ID       uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:(uuid_generate_v4())"`
	UserID   uuid.UUID `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_ai_provider_user"`
	Provider string    `json:"provider" gorm:"not null;uniqueIndex:idx_ai_provider_user"`
	// Model overrides the provider's default model; empty = default.
	Model string `json:"model"`
	// BaseURL overrides the provider's API endpoint (required for a remote
	// Ollama server); empty = default.
	BaseURL         string    `json:"base_url"`
	EncryptedAPIKey string    `json:"-" gorm:"column:api_key;type:text"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
