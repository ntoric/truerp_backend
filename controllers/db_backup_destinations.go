package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"truerp/models"
	"truerp/utils"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	mega "github.com/t3rm1n4l/go-mega"
)

var dbBackupHTTPClient = &http.Client{Timeout: 15 * time.Minute}

// uploadDBBackupToDestination ships a produced dump to the configured cloud
// destination. "local" keeps the file on disk only and always succeeds.
// Returns a short human-readable detail string on success.
func uploadDBBackupToDestination(s *models.DBBackupSettings, filePath string) (string, error) {
	switch dbBackupDestinationOrDefault(s.DestinationType) {
	case "local":
		return "stored locally", nil
	case "s3":
		return uploadBackupToS3(s, filePath)
	case "gdrive":
		return uploadBackupToGDrive(s, filePath)
	case "mega":
		return uploadBackupToMega(s, filePath)
	case "telegram":
		return uploadBackupToTelegram(s, filePath)
	case "custom":
		return uploadBackupToCustom(s, filePath)
	default:
		return "", fmt.Errorf("unknown backup destination %q", s.DestinationType)
	}
}

// decryptDBBackupSecret unwraps a stored encrypted secret and reports a
// friendly error when the field is empty or cannot be decrypted.
func decryptDBBackupSecret(ciphertext, label string) (string, error) {
	if ciphertext == "" {
		return "", fmt.Errorf("%s is not configured", label)
	}
	plain, err := utils.Decrypt(ciphertext)
	if err != nil || plain == "" {
		return "", fmt.Errorf("failed to decrypt %s — re-enter it in settings", label)
	}
	return plain, nil
}

// ---------------------------------------------------------------------------
// S3-compatible (AWS S3, Cloudflare R2, MinIO, DigitalOcean Spaces, ...)
// ---------------------------------------------------------------------------

func uploadBackupToS3(s *models.DBBackupSettings, filePath string) (string, error) {
	if s.S3Bucket == "" {
		return "", fmt.Errorf("S3 bucket is required")
	}
	if s.S3AccessKey == "" {
		return "", fmt.Errorf("S3 access key is required")
	}
	secret, err := decryptDBBackupSecret(s.S3SecretKey, "S3 secret key")
	if err != nil {
		return "", err
	}

	region := s.S3Region
	if region == "" {
		region = "auto"
	}
	cfg := &aws.Config{
		Region:      aws.String(region),
		Credentials: credentials.NewStaticCredentials(s.S3AccessKey, secret, ""),
	}
	if endpoint := strings.TrimSpace(s.S3Endpoint); endpoint != "" {
		cfg.Endpoint = aws.String(endpoint)
		cfg.S3ForcePathStyle = aws.Bool(true) // MinIO/R2-compatible endpoints
	}

	sess, err := session.NewSession(cfg)
	if err != nil {
		return "", fmt.Errorf("S3 session: %w", err)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	key := strings.Trim(s.S3Prefix, "/")
	if key != "" {
		key += "/"
	}
	key += filepath.Base(filePath)

	if _, err := s3.New(sess).PutObject(&s3.PutObjectInput{
		Bucket: aws.String(s.S3Bucket),
		Key:    aws.String(key),
		Body:   f,
	}); err != nil {
		return "", fmt.Errorf("S3 upload: %w", err)
	}
	return fmt.Sprintf("s3://%s/%s", s.S3Bucket, key), nil
}

// ---------------------------------------------------------------------------
// Google Drive — service-account JWT → access token → multipart file upload.
// ---------------------------------------------------------------------------

type gdriveServiceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func gdriveAccessToken(sa gdriveServiceAccount) (string, error) {
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return "", fmt.Errorf("service-account JSON is missing client_email/private_key")
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return "", fmt.Errorf("invalid service-account private_key: %w", err)
	}

	now := time.Now()
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   sa.ClientEmail,
		"scope": "https://www.googleapis.com/auth/drive.file",
		"aud":   tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign service-account JWT: %w", err)
	}

	resp, err := dbBackupHTTPClient.PostForm(tokenURI, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	})
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		if tok.Error != "" {
			return "", fmt.Errorf("token response: %s", tok.Error)
		}
		return "", fmt.Errorf("token response missing access_token")
	}
	return tok.AccessToken, nil
}

func uploadBackupToGDrive(s *models.DBBackupSettings, filePath string) (string, error) {
	credsJSON, err := decryptDBBackupSecret(s.GDriveServiceAccountJSON, "Google Drive service-account JSON")
	if err != nil {
		return "", err
	}
	var sa gdriveServiceAccount
	if err := json.Unmarshal([]byte(credsJSON), &sa); err != nil {
		return "", fmt.Errorf("service-account JSON is not valid JSON: %w", err)
	}
	token, err := gdriveAccessToken(sa)
	if err != nil {
		return "", err
	}

	metadata := map[string]interface{}{"name": filepath.Base(filePath)}
	if folder := strings.TrimSpace(s.GDriveFolderID); folder != "" {
		metadata["parents"] = []string{folder}
	}
	metaJSON, _ := json.Marshal(metadata)

	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		metaPart, err := mw.CreatePart(map[string][]string{
			"Content-Type": {"application/json; charset=UTF-8"},
		})
		if err == nil {
			_, err = metaPart.Write(metaJSON)
		}
		if err == nil {
			var filePart io.Writer
			filePart, err = mw.CreateFormFile("file", filepath.Base(filePath))
			if err == nil {
				_, err = io.Copy(filePart, f)
			}
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()

	req, err := http.NewRequest(http.MethodPost,
		"https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&fields=id,name,webViewLink", pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := dbBackupHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Drive upload: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("Drive upload failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	json.Unmarshal(body, &created)
	return fmt.Sprintf("gdrive:%s (id %s)", created.Name, created.ID), nil
}

// ---------------------------------------------------------------------------
// Mega — go-mega client (login + upload to root folder).
// ---------------------------------------------------------------------------

func uploadBackupToMega(s *models.DBBackupSettings, filePath string) (string, error) {
	if s.MegaEmail == "" {
		return "", fmt.Errorf("Mega email is required")
	}
	password, err := decryptDBBackupSecret(s.MegaPassword, "Mega password")
	if err != nil {
		return "", err
	}

	m := mega.New()
	m.SetTimeOut(10 * time.Minute)
	if err := m.Login(s.MegaEmail, password); err != nil {
		return "", fmt.Errorf("Mega login: %w", err)
	}
	node, err := m.UploadFile(filePath, m.FS.GetRoot(), filepath.Base(filePath), nil)
	if err != nil {
		return "", fmt.Errorf("Mega upload: %w", err)
	}
	return fmt.Sprintf("mega://%s", node.GetName()), nil
}

// ---------------------------------------------------------------------------
// Telegram bot — sendDocument API.
// ---------------------------------------------------------------------------

func uploadBackupToTelegram(s *models.DBBackupSettings, filePath string) (string, error) {
	if s.TelegramChatID == "" {
		return "", fmt.Errorf("Telegram chat ID is required")
	}
	token, err := decryptDBBackupSecret(s.TelegramBotToken, "Telegram bot token")
	if err != nil {
		return "", err
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", token)
	body, status, err := postMultipartFile(apiURL, "document", filePath, map[string]string{
		"chat_id": s.TelegramChatID,
		"caption": "TruERP database backup — " + filepath.Base(filePath),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("Telegram upload: %w", err)
	}
	var parsed struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.Unmarshal(body, &parsed)
	if status/100 != 2 || !parsed.OK {
		desc := parsed.Description
		if desc == "" {
			desc = strings.TrimSpace(string(body))
		}
		return "", fmt.Errorf("Telegram upload failed (%d): %s", status, desc)
	}
	return fmt.Sprintf("telegram chat %s", s.TelegramChatID), nil
}

// ---------------------------------------------------------------------------
// Custom application — POST the dump as multipart field "file" to any URL.
// ---------------------------------------------------------------------------

func uploadBackupToCustom(s *models.DBBackupSettings, filePath string) (string, error) {
	target := strings.TrimSpace(s.CustomURL)
	if target == "" {
		return "", fmt.Errorf("custom endpoint URL is required")
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return "", fmt.Errorf("custom endpoint URL must start with http:// or https://")
	}

	headers := map[string]string{}
	if s.CustomHeaders != "" {
		var extra map[string]string
		if err := json.Unmarshal([]byte(s.CustomHeaders), &extra); err != nil {
			return "", fmt.Errorf("custom headers must be a JSON object: %w", err)
		}
		for k, v := range extra {
			headers[k] = v
		}
	}
	if s.CustomAuthHeader != "" {
		auth, err := decryptDBBackupSecret(s.CustomAuthHeader, "custom auth header")
		if err != nil {
			return "", err
		}
		headers["Authorization"] = auth
	}

	body, status, err := postMultipartFile(target, "file", filePath, nil, headers)
	if err != nil {
		return "", fmt.Errorf("custom endpoint upload: %w", err)
	}
	if status/100 != 2 {
		return "", fmt.Errorf("custom endpoint returned %d: %s", status,
			strings.TrimSpace(string(bytes.TrimSpace(body))))
	}
	return fmt.Sprintf("custom endpoint %s (%d)", target, status), nil
}

// postMultipartFile streams a file as a multipart form field, plus optional
// scalar form fields and request headers, returning the response body and
// status code.
func postMultipartFile(target, fieldName, filePath string, fields, headers map[string]string) ([]byte, int, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var wErr error
		for k, v := range fields {
			if wErr = mw.WriteField(k, v); wErr != nil {
				break
			}
		}
		if wErr == nil {
			var part io.Writer
			part, wErr = mw.CreateFormFile(fieldName, filepath.Base(filePath))
			if wErr == nil {
				_, wErr = io.Copy(part, f)
			}
		}
		if wErr == nil {
			wErr = mw.Close()
		}
		pw.CloseWithError(wErr)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, pr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := dbBackupHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, nil
}

// ---------------------------------------------------------------------------
// Test destination endpoint — uploads a tiny probe file through the same code
// path as a real backup so configuration mistakes surface immediately.
// ---------------------------------------------------------------------------

func TestDBBackupDestinationHandler(c *gin.Context) {
	settings, err := GetOrCreateDBBackupSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load backup settings"})
		return
	}

	// The form may contain unsaved edits — apply them over the stored row so
	// users can test before saving. Never persisted.
	var input dbBackupSettingsInput
	if err := c.ShouldBindJSON(&input); err == nil {
		tmp := settings
		if applyErr := applyDBBackupSettingsInput(&tmp, input); applyErr == nil {
			settings = tmp
		}
	}

	if err := validateDBBackupDestination(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if dbBackupDestinationOrDefault(settings.DestinationType) == "local" {
		dir := dbBackupDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("backup directory not writable: %v", err)})
			return
		}
		probe := filepath.Join(dir, ".write-test")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("backup directory not writable: %v", err)})
			return
		}
		os.Remove(probe)
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Local directory %s is writable", dir)})
		return
	}

	probePath := filepath.Join(os.TempDir(), fmt.Sprintf("truerp-backup-test-%d.txt", time.Now().Unix()))
	if err := os.WriteFile(probePath,
		[]byte("TruERP backup destination test — safe to delete.\n"), 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create test file"})
		return
	}
	defer os.Remove(probePath)

	detail, err := uploadDBBackupToDestination(&settings, probePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Upload successful — %s", detail)})
}
