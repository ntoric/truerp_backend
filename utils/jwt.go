package utils

import (
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
	jwt.RegisteredClaims
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
	})
	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.New("invalid token claims")
}
