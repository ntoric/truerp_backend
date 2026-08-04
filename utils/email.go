package utils

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strconv"
	"strings"
)

type EmailConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
}

func GetEmailConfig() EmailConfig {
	port, _ := strconv.Atoi(os.Getenv("SMTP_PORT"))
	if port == 0 {
		port = 587
	}
	fromName := os.Getenv("SMTP_FROM_NAME")
	if fromName == "" {
		fromName = "TruERP"
	}
	return EmailConfig{
		Host:     strings.TrimSpace(os.Getenv("SMTP_HOST")),
		Port:     port,
		Username: strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     strings.TrimSpace(os.Getenv("SMTP_FROM_EMAIL")),
		FromName: fromName,
	}
}

func EmailConfigured() bool {
	cfg := GetEmailConfig()
	return cfg.Host != "" && cfg.From != ""
}

func FrontendURL() string {
	url := strings.TrimSpace(os.Getenv("FRONTEND_URL"))
	if url == "" {
		return "http://localhost:3000"
	}
	return strings.TrimRight(url, "/")
}

func SendEmail(to, subject, body string) error {
	cfg := GetEmailConfig()
	if !EmailConfigured() {
		return fmt.Errorf("email not configured")
	}

	from := cfg.From
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", cfg.FromName, cfg.From)
	}

	msg := strings.Join([]string{
		fmt.Sprintf("From: %s", from),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if err := smtp.SendMail(addr, auth, cfg.From, []string{to}, []byte(msg)); err != nil {
		return err
	}
	return nil
}

func SendPasswordResetEmail(to, resetURL string) error {
	subject := "Reset your TruERP password"
	body := fmt.Sprintf(`<p>Hello,</p>
<p>We received a request to reset your TruERP account password.</p>
<p><a href="%s">Reset your password</a></p>
<p>This link expires in 1 hour. If you did not request a password reset, you can ignore this email.</p>
<p>— TruERP</p>`, resetURL)

	if err := SendEmail(to, subject, body); err != nil {
		return err
	}
	return nil
}

func LogPasswordResetLink(email, resetURL string) {
	log.Printf("[password-reset] email=%s reset_url=%s (SMTP not configured — use this link for testing)", email, resetURL)
}
