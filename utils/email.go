package utils

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
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
	return SendEmailWithConfig(cfg, to, subject, body)
}

func SendEmailWithConfig(cfg EmailConfig, to, subject, body string) error {
	if cfg.Host == "" || cfg.From == "" {
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

func TestSMTPConnection(cfg EmailConfig) error {
	if cfg.Host == "" {
		return fmt.Errorf("SMTP host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{ServerName: cfg.Host}
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}

	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err = client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP authentication failed: %w", err)
		}
	}

	return nil
}

func SendPasswordResetOTPEmail(to, otp string) error {
	subject := "Your TruERP password reset code"
	body := fmt.Sprintf(`<p>Hello,</p>
<p>We received a request to reset your TruERP account password.</p>
<p>Your verification code is: <strong style="font-size:24px;letter-spacing:4px;">%s</strong></p>
<p>This code expires in 15 minutes. If you did not request a password reset, you can ignore this email.</p>
<p>— TruERP</p>`, otp)

	if err := SendEmail(to, subject, body); err != nil {
		return err
	}
	return nil
}

func LogPasswordResetOTP(email, otp string) {
	log.Printf("[password-reset] email=%s otp=%s (SMTP not configured — use this code for testing)", email, otp)
}
