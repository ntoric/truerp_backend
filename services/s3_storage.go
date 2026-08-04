package services

import (
	"io"
	"mime/multipart"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// S3Storage implements StorageService for AWS S3 storage
type S3Storage struct {
	s3Client *s3.S3
	bucket   string
	baseURL  string
}

// NewS3Storage creates a new S3Storage instance
func NewS3Storage(bucket, region, baseURL string) (*S3Storage, error) {
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(region),
	})
	if err != nil {
		return nil, err
	}

	return &S3Storage{
		s3Client: s3.New(sess),
		bucket:   bucket,
		baseURL:  baseURL,
	}, nil
}

// NewS3StorageWithEndpoint creates a new S3Storage instance with custom endpoint (for S3-compatible services like Cloudflare R2)
func NewS3StorageWithEndpoint(bucket, accountID, accessKeyID, secretAccessKey, baseURL string) (*S3Storage, error) {
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String("auto"),
		Credentials: credentials.NewStaticCredentials(accessKeyID, secretAccessKey, ""),
		Endpoint: aws.String("https://" + accountID + ".r2.cloudflarestorage.com"),
	})
	if err != nil {
		return nil, err
	}

	return &S3Storage{
		s3Client: s3.New(sess),
		bucket:   bucket,
		baseURL:  baseURL,
	}, nil
}

// UploadFile uploads a file to S3
func (s3s *S3Storage) UploadFile(file *multipart.FileHeader, path string) (string, error) {
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()

	_, err = s3s.s3Client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(s3s.bucket),
		Key:    aws.String(path),
		Body:   src,
		ACL:    aws.String("public-read"),
	})
	if err != nil {
		return "", err
	}

	return s3s.GetFileURL(path), nil
}

// DeleteFile removes a file from S3
func (s3s *S3Storage) DeleteFile(path string) error {
	_, err := s3s.s3Client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(s3s.bucket),
		Key:    aws.String(path),
	})
	return err
}

// GetFileURL returns the public URL for a file
func (s3s *S3Storage) GetFileURL(path string) string {
	cleanPath := strings.TrimPrefix(path, "/")
	return strings.TrimSuffix(s3s.baseURL, "/") + "/" + cleanPath
}

// GetFile retrieves a file from S3
func (s3s *S3Storage) GetFile(path string) (io.ReadCloser, error) {
	result, err := s3s.s3Client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(s3s.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}
