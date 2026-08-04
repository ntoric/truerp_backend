package services

import (
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalStorage implements StorageService for local file system storage
type LocalStorage struct {
	basePath string
	baseURL  string
}

// NewLocalStorage creates a new LocalStorage instance
func NewLocalStorage(basePath, baseURL string) *LocalStorage {
	return &LocalStorage{
		basePath: basePath,
		baseURL:  baseURL,
	}
}

// UploadFile saves a file to local storage
func (ls *LocalStorage) UploadFile(file *multipart.FileHeader, path string) (string, error) {
	// Create full file path
	fullPath := filepath.Join(ls.basePath, path)
	
	// Create directory if it doesn't exist
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	
	// Open the uploaded file
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	
	// Create destination file
	dst, err := os.Create(fullPath)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	
	// Copy file content
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	
	// Return public URL
	return ls.GetFileURL(path), nil
}

// DeleteFile removes a file from local storage
func (ls *LocalStorage) DeleteFile(path string) error {
	fullPath := filepath.Join(ls.basePath, path)
	return os.Remove(fullPath)
}

// GetFileURL returns the public URL for a file
func (ls *LocalStorage) GetFileURL(path string) string {
	// Clean the path to prevent directory traversal
	cleanPath := strings.TrimPrefix(path, "/")
	cleanPath = filepath.ToSlash(filepath.Clean(cleanPath))
	return strings.TrimSuffix(ls.baseURL, "/") + "/" + cleanPath
}

// GetFile retrieves a file from local storage
func (ls *LocalStorage) GetFile(path string) (io.ReadCloser, error) {
	fullPath := filepath.Join(ls.basePath, path)
	return os.Open(fullPath)
}

// GenerateUniquePath generates a unique file path with timestamp
func GenerateUniquePath(originalFilename string) string {
	ext := filepath.Ext(originalFilename)
	name := strings.TrimSuffix(originalFilename, ext)
	timestamp := time.Now().Format("20060102-150405")
	return filepath.Join(timestamp, name+ext)
}
