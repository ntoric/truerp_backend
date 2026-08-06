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
	if cfg.Port == 0 {
		cfg.Port = 587
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

	client, err := dialSMTPClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err = client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP authentication failed: %w", err)
		}
	}

	if err = client.Mail(cfg.From); err != nil {
		return fmt.Errorf("SMTP MAIL FROM failed: %w", err)
	}
	if err = client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO failed: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %w", err)
	}
	if _, err = w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("SMTP message close failed: %w", err)
	}

	return client.Quit()
}

// dialSMTPConn opens a TCP connection to the SMTP server.
// Port 465 uses implicit TLS (SMTPS); other ports use plain TCP and STARTTLS when available.
func dialSMTPConn(cfg EmailConfig) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	if cfg.Port == 465 {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SMTP server (TLS): %w", err)
		}
		return conn, nil
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	return conn, nil
}

func dialSMTPClient(cfg EmailConfig) (*smtp.Client, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("SMTP host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}

	conn, err := dialSMTPConn(cfg)
	if err != nil {
		return nil, err
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create SMTP client: %w", err)
	}

	// Port 465 is already TLS-wrapped; other ports upgrade via STARTTLS when supported.
	if cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			tlsConfig := &tls.Config{ServerName: cfg.Host}
			if err = client.StartTLS(tlsConfig); err != nil {
				client.Close()
				return nil, fmt.Errorf("STARTTLS failed: %w", err)
			}
		}
	}

	return client, nil
}

func TestSMTPConnection(cfg EmailConfig) error {
	if cfg.Port == 0 {
		cfg.Port = 587
	}

	client, err := dialSMTPClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

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
