package utils

import (
	"os"
	"testing"
)

func TestEncryptDecryptWithNonStandardKeyLength(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "change-me-to-a-long-random-string")

	encrypted, err := Encrypt("smtp-secret")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	plain, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if plain != "smtp-secret" {
		t.Fatalf("expected smtp-secret, got %q", plain)
	}
}

func TestEncryptDecryptWith32ByteKey(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "truerp-encryption-key-32-bytes!!")

	encrypted, err := Encrypt("smtp-secret")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	plain, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if plain != "smtp-secret" {
		t.Fatalf("expected smtp-secret, got %q", plain)
	}
}

func TestEncryptWithEmptyKeyUsesDefault(t *testing.T) {
	os.Unsetenv("ENCRYPTION_KEY")

	encrypted, err := Encrypt("smtp-secret")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	plain, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if plain != "smtp-secret" {
		t.Fatalf("expected smtp-secret, got %q", plain)
	}
}
