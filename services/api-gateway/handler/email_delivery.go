package handler

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Only definite non-acceptance permits another provider to send the same message.
var errEmailUnavailable = errors.New("email provider did not accept message")

type emailDeliverySettings struct {
	ResendEnabled   bool   `json:"resendEnabled"`
	SMTPEnabled     bool   `json:"smtpEnabled"`
	PrimaryProvider string `json:"primaryProvider"`
	FallbackEnabled bool   `json:"fallbackEnabled"`
	Host            string `json:"smtpHost"`
	Port            int    `json:"smtpPort"`
	TLSMode         string `json:"smtpTlsMode"`
	Username        string `json:"smtpUsername"`
	Password        string `json:"smtpPassword,omitempty"`
	FromEmail       string `json:"smtpFromEmail"`
	FromName        string `json:"smtpFromName"`
	Ciphertext      []byte `json:"-"`
}

func readEmailDeliverySettings(ctx context.Context, pool *pgxpool.Pool) (emailDeliverySettings, error) {
	s := emailDeliverySettings{ResendEnabled: true, PrimaryProvider: "resend", Port: 587, TLSMode: "starttls", FromName: "HeyGo"}
	err := pool.QueryRow(ctx, `SELECT resend_enabled,smtp_enabled,primary_provider,fallback_enabled,smtp_host,smtp_port,smtp_tls_mode,smtp_username,smtp_password_ciphertext,smtp_from_email,smtp_from_name FROM admin_email_delivery_settings WHERE id=1`).Scan(&s.ResendEnabled, &s.SMTPEnabled, &s.PrimaryProvider, &s.FallbackEnabled, &s.Host, &s.Port, &s.TLSMode, &s.Username, &s.Ciphertext, &s.FromEmail, &s.FromName)
	if err == pgx.ErrNoRows {
		err = nil
	}
	return s, err
}
func deliverEmail(primary string, fallback bool, resendEnabled, smtpEnabled bool, send func(string) error) error {
	secondary := "smtp"
	if primary == "smtp" {
		secondary = "resend"
	}
	enabled := func(p string) bool {
		if p == "smtp" {
			return smtpEnabled
		}
		return resendEnabled
	}
	err := errEmailUnavailable
	if enabled(primary) {
		err = send(primary)
	}
	if err != nil && errors.Is(err, errEmailUnavailable) && fallback && enabled(secondary) {
		return send(secondary)
	}
	return err
}
func sendConfiguredEmail(ctx context.Context, pool *pgxpool.Pool, key []byte, to, subject, body, html string) error {
	s, err := readEmailDeliverySettings(ctx, pool)
	if err != nil {
		return err
	}
	return deliverEmail(s.PrimaryProvider, s.FallbackEnabled, s.ResendEnabled, s.SMTPEnabled, func(provider string) error {
		if provider == "smtp" {
			if s.Host == "" || !validResendEmail(s.FromEmail) {
				return errEmailUnavailable
			}
			password := ""
			if len(s.Ciphertext) > 0 {
				password, err = openAdminValue(key, s.Ciphertext, "heygo-smtp-password-v1")
				if err != nil {
					return errEmailUnavailable
				}
			}
			return sendSMTPEmail(ctx, s, password, to, subject, body)
		}
		r, ok, e := readResendSettingsPool(ctx, pool)
		if e != nil {
			return e
		}
		if !ok {
			return errEmailUnavailable
		}
		secret, e := openAdminSecret(key, r.APIKeyCiphertext)
		if e != nil {
			return errEmailUnavailable
		}
		return sendResendEmail(ctx, secret, r.FromEmail, r.FromName, to, subject, body, html)
	})
}

// TLS is mandatory: implicit TLS or required STARTTLS. No plaintext credential mode.
func sendSMTPEmail(ctx context.Context, s emailDeliverySettings, password, to, subject, body string) error {
	return sendSMTPEmailTLS(ctx, s, password, to, subject, body, &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12})
}

func sendSMTPEmailTLS(ctx context.Context, s emailDeliverySettings, password, to, subject, body string, tlsConfig *tls.Config) error {
	if !validResendEmail(to) || !validResendEmail(s.FromEmail) || strings.ContainsAny(subject+s.FromName, "\r\n") {
		return fmt.Errorf("invalid email envelope")
	}
	address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("%w: SMTP connection", errEmailUnavailable)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	if s.TLSMode == "tls" {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("%w: SMTP TLS", errEmailUnavailable)
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return errEmailUnavailable
	}
	defer client.Close()
	if s.TLSMode == "starttls" {
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("%w: SMTP STARTTLS", errEmailUnavailable)
		}
	} else if s.TLSMode != "tls" {
		return errEmailUnavailable
	}
	if s.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.Username, password, s.Host)); err != nil {
			return fmt.Errorf("%w: SMTP authentication", errEmailUnavailable)
		}
	}
	if err = client.Mail(s.FromEmail); err != nil {
		return errEmailUnavailable
	}
	if err = client.Rcpt(to); err != nil {
		return errEmailUnavailable
	}
	writer, err := client.Data()
	if err != nil {
		return errEmailUnavailable
	}
	from := (&mail.Address{Name: s.FromName, Address: s.FromEmail}).String()
	message := "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if _, err = writer.Write([]byte(message)); err != nil {
		return fmt.Errorf("SMTP outcome unresolved")
	}
	if err = writer.Close(); err != nil {
		return fmt.Errorf("SMTP outcome unresolved")
	}
	// Successful DATA response is acceptance; a lost QUIT response cannot undo it.
	_ = client.Quit()
	return nil
}
