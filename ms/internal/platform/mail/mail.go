// Package mail sends transactional e-mail (one-time codes).
package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Sender delivers a plain-text e-mail.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// SMTPSender delivers over SMTP: implicit TLS on port 465, STARTTLS otherwise
// (net/smtp upgrades automatically when the server offers it, and refuses to
// send credentials over an unencrypted connection to a remote host).
type SMTPSender struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// Send implements Sender.
func (s SMTPSender) Send(ctx context.Context, to, subject, body string) error {
	if strings.ContainsAny(to+subject, "\r\n") {
		return fmt.Errorf("mail: header injection attempt")
	}
	msg := strings.Join([]string{
		"From: " + s.From,
		"To: " + to,
		"Subject: " + subject,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Password, s.Host)
	}

	if s.Port != 465 {
		return smtp.SendMail(addr, auth, s.From, []string{to}, []byte(msg))
	}

	d := tls.Dialer{Config: &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(s.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// LogSender writes e-mails to the log instead of sending them. Development
// only: config.Load refuses it in production because it prints codes.
type LogSender struct {
	Log *slog.Logger
}

// Send implements Sender.
func (l LogSender) Send(_ context.Context, to, subject, body string) error {
	l.Log.Warn("DEV MAIL (not sent)", "to", to, "subject", subject, "body", body)
	return nil
}
