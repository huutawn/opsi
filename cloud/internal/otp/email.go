package otp

import (
	"context"
	"fmt"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SMTPConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

type SMTPSender struct {
	Config SMTPConfig
}

func (s SMTPSender) SendOTP(ctx context.Context, req Request, code string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.Email == "" {
		return fmt.Errorf("otp user email is required")
	}
	addr := s.Config.Host
	if s.Config.Port != "" {
		addr += ":" + s.Config.Port
	}
	if addr == "" || s.Config.From == "" {
		return fmt.Errorf("smtp host and from are required")
	}
	message := strings.Join([]string{
		"From: " + s.Config.From,
		"To: " + req.Email,
		"Subject: Opsi OTP code",
		"Content-Type: text/plain; charset=utf-8",
		"",
		fmt.Sprintf("Your Opsi OTP code is %s. It expires at %s.", code, expiresAt.UTC().Format(time.RFC3339)),
	}, "\r\n")
	var auth smtp.Auth
	if s.Config.Username != "" || s.Config.Password != "" {
		auth = smtp.PlainAuth("", s.Config.Username, s.Config.Password, s.Config.Host)
	}
	return smtp.SendMail(addr, auth, s.Config.From, []string{req.Email}, []byte(message))
}

type FileOutboxSender struct {
	Path string
}

func (s FileOutboxSender) SendOTP(ctx context.Context, req Request, code string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	line := fmt.Sprintf("%s project=%s user=%s email=%s purpose=%s expires_at=%s code=%s\n", time.Now().UTC().Format(time.RFC3339), req.ProjectID, req.UserID, req.Email, req.Purpose, expiresAt.UTC().Format(time.RFC3339), code)
	file, err := os.OpenFile(s.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(line)
	return err
}
