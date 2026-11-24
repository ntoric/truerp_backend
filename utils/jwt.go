package utils

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var jwtSecret = []byte("truerp-super-secret-key-change-in-production")

func SetJWTSecret(secret string) {
	jwtSecret = []byte(secret)
}

type Claims struct {
	UserID   uuid.UUID  `json:"user_id"`
	PartyID  uuid.UUID  `json:"party_id,omitempty"`
	StoreID  *uuid.UUID `json:"store_id,omitempty"`
	UserName string     `json:"user_name"`
	Email    string     `json:"email"`
	Role     string     `json:"role"`
	// Scope is set only on OAuth-issued access tokens: a space-separated list
	// of granted scopes (see utils/scopes.go). Empty means a first-party
	// session token with the user's full access.
	Scope string `json:"scope,omitempty"`
	// ClientID identifies the OAuth client an access token was issued to.
	ClientID string `json:"client_id,omitempty"`
	jwt.RegisteredClaims
}

// OAuthAccessTokenTTL is the lifetime of OAuth-issued access tokens.
const OAuthAccessTokenTTL = time.Hour

// GenerateOAuthAccessToken issues a short-lived access token bound to an
// OAuth client and a set of granted scopes.
func GenerateOAuthAccessToken(userID uuid.UUID, userName, email, role string, storeID *uuid.UUID, clientID, scope string) (string, error) {
	claims := Claims{
		UserID:   userID,
		StoreID:  storeID,
		UserName: userName,
		Email:    email,
		Role:     role,
		Scope:    scope,
		ClientID: clientID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(OAuthAccessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        uuid.NewString(),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func GenerateToken(userID uuid.UUID, userName, email, role string, storeID *uuid.UUID) (string, error) {
	claims := Claims{
		UserID:   userID,
		StoreID:  storeID,
		UserName: userName,
		Email:    email,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func GeneratePortalToken(userID, partyID uuid.UUID, partyName, phone string) (string, error) {
	claims := Claims{
		UserID:   userID,
		PartyID:  partyID,
		UserName: partyName,
		Email:    phone,
		Role:     "portal",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.New("invalid token claims")
}

// SignHMAC returns an HMAC-SHA256 of data keyed by the server secret and a
// purpose label, for short unguessable public links (e.g. invoice PDFs).
func SignHMAC(purpose string, data []byte) []byte {
	mac := hmac.New(sha256.New, append(append([]byte{}, jwtSecret...), []byte("|"+purpose)...))
	mac.Write(data)
	return mac.Sum(nil)
}
