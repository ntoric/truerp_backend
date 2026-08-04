package services

import (
	"log"
	"os"
)

// StorageType defines the type of storage backend
type StorageType string

const (
	StorageTypeLocal   StorageType = "local"
	StorageTypeS3      StorageType = "s3"
	StorageTypeCloudflareR2 StorageType = "cloudflare_r2"
)

// StorageConfig holds configuration for storage services
type StorageConfig struct {
	Type         StorageType
	LocalPath    string
	LocalBaseURL string
	S3Bucket     string
	S3Region     string
	S3BaseURL    string
	// Cloudflare R2 configuration (S3-compatible)
	R2AccountID      string
	R2AccessKeyID    string
	R2SecretAccessKey string
	R2Bucket         string
	R2BaseURL        string
}

// GetStorageConfig returns storage configuration from environment variables
func GetStorageConfig() StorageConfig {
	storageType := StorageType(os.Getenv("STORAGE_TYPE"))
	if storageType == "" {
		storageType = StorageTypeLocal // Default to local storage
	}

	return StorageConfig{
		Type:             storageType,
		LocalPath:        os.Getenv("LOCAL_STORAGE_PATH"),
		LocalBaseURL:     os.Getenv("LOCAL_STORAGE_BASE_URL"),
		S3Bucket:         os.Getenv("S3_BUCKET"),
		S3Region:         os.Getenv("S3_REGION"),
		S3BaseURL:        os.Getenv("S3_BASE_URL"),
		R2AccountID:      os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:    os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket:         os.Getenv("R2_BUCKET"),
		R2BaseURL:        os.Getenv("R2_BASE_URL"),
	}
}

// NewStorageService creates a StorageService based on configuration
func NewStorageService(config StorageConfig) (StorageService, error) {
	switch config.Type {
	case StorageTypeLocal:
		localPath := config.LocalPath
		if localPath == "" {
			localPath = "uploads" // Default path
		}
		localBaseURL := config.LocalBaseURL
		if localBaseURL == "" {
			localBaseURL = "/uploads" // Default base URL
		}
		return NewLocalStorage(localPath, localBaseURL), nil

	case StorageTypeS3:
		if config.S3Bucket == "" || config.S3Region == "" {
			panic("S3_BUCKET and S3_REGION environment variables must be set for S3 storage")
		}
		s3BaseURL := config.S3BaseURL
		if s3BaseURL == "" {
			s3BaseURL = "https://" + config.S3Bucket + ".s3." + config.S3Region + ".amazonaws.com"
		}
		return NewS3Storage(config.S3Bucket, config.S3Region, s3BaseURL)

	case StorageTypeCloudflareR2:
		if config.R2Bucket == "" || config.R2AccountID == "" {
			panic("R2_BUCKET and R2_ACCOUNT_ID environment variables must be set for Cloudflare R2 storage")
		}
		r2BaseURL := config.R2BaseURL
		if r2BaseURL == "" {
			r2BaseURL = "https://" + config.R2Bucket + "." + config.R2AccountID + ".r2.cloudflarestorage.com"
		}
		// Cloudflare R2 is S3-compatible, so we can use the S3 storage implementation
		// We need to set custom endpoint for R2
		return NewS3StorageWithEndpoint(config.R2Bucket, config.R2AccountID, config.R2AccessKeyID, config.R2SecretAccessKey, r2BaseURL)

	default:
		// Default to local storage
		return NewLocalStorage("uploads", "/uploads"), nil
	}
}

// GetDefaultStorageService returns the configured storage service
func GetDefaultStorageService() StorageService {
	config := GetStorageConfig()
	service, err := NewStorageService(config)
	if err != nil {
		log.Printf("Failed to initialize storage service: %v", err)
		log.Println("Falling back to local storage")
		// Fallback to local storage
		return NewLocalStorage("uploads", "/uploads")
	}
	return service
}
