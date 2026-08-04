package services

import (
	"io"
	"mime/multipart"
)

// StorageService interface defines the contract for storage implementations
type StorageService interface {
	// UploadFile uploads a file and returns the public URL
	UploadFile(file *multipart.FileHeader, path string) (string, error)
	
	// DeleteFile removes a file from storage
	DeleteFile(path string) error
	
	// GetFileURL returns the public URL for a file
	GetFileURL(path string) string
	
	// GetFile retrieves a file from storage
	GetFile(path string) (io.ReadCloser, error)
}
