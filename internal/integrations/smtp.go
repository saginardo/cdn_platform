package integrations

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type Notifier interface {
	Notify(ctx context.Context, subject, body string) error
}

const (
	SMTPSecurityStartTLS = "starttls"
	SMTPSecurityTLS      = "tls"
)

type SMTPNotifier struct {
	Host      string
	Port      int
	Username  string
	Password  string
	From      string
	To        []string
	Security  string
	Timeout   time.Duration
	TLSConfig *tls.Config
}

func (s SMTPNotifier) Validate() error {
	if strings.TrimSpace(s.Host) == "" || strings.ContainsAny(s.Host, "\r\n") {
		return fmt.Errorf("SMTP host is required")
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("SMTP port must be between 1 and 65535")
	}
	if s.Security != SMTPSecurityStartTLS && s.Security != SMTPSecurityTLS {
		return fmt.Errorf("SMTP security must be starttls or tls")
	}
	if strings.TrimSpace(s.From) == "" {
		return fmt.Errorf("SMTP from address is required")
	}
	if _, err := parseMailbox(s.From); err != nil {
		return fmt.Errorf("SMTP from address: %w", err)
	}
	if len(s.To) == 0 {
		return fmt.Errorf("at least one SMTP recipient is required")
	}
	for _, recipient := range s.To {
		if _, err := parseMailbox(recipient); err != nil {
			return fmt.Errorf("SMTP recipient: %w", err)
		}
	}
	if strings.ContainsAny(s.Username, "\r\n") {
		return fmt.Errorf("SMTP username contains invalid characters")
	}
	return nil
}

func (s SMTPNotifier) Notify(ctx context.Context, subject, body string) error {
	if len(s.To) == 0 {
		return nil
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("SMTP subject contains invalid characters")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	address := net.JoinHostPort(strings.TrimSpace(s.Host), strconv.Itoa(s.Port))
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	connection, err := dialer.DialContext(dialCtx, "tcp", address)
	if err != nil {
		return err
	}
	defer connection.Close()
	if deadline, ok := dialCtx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	stopCancel := context.AfterFunc(dialCtx, func() { _ = connection.Close() })
	defer stopCancel()

	tlsConfig := &tls.Config{ServerName: strings.TrimSpace(s.Host), MinVersion: tls.VersionTLS12}
	if s.TLSConfig != nil {
		tlsConfig = s.TLSConfig.Clone()
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = strings.TrimSpace(s.Host)
		}
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	if s.Security == SMTPSecurityTLS {
		tlsConnection := tls.Client(connection, tlsConfig)
		if err := tlsConnection.HandshakeContext(dialCtx); err != nil {
			return err
		}
		connection = tlsConnection
	}
	client, err := smtp.NewClient(connection, strings.TrimSpace(s.Host))
	if err != nil {
		return err
	}
	defer client.Close()
	if s.Security == SMTPSecurityStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, strings.TrimSpace(s.Host))); err != nil {
			return err
		}
	}
	from, _ := parseMailbox(s.From)
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	recipients := make([]*mail.Address, 0, len(s.To))
	for _, value := range s.To {
		recipient, _ := parseMailbox(value)
		if err := client.Rcpt(recipient.Address); err != nil {
			return err
		}
		recipients = append(recipients, recipient)
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	toHeader := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		toHeader = append(toHeader, recipient.String())
	}
	message := "From: " + from.String() + "\r\nTo: " + strings.Join(toHeader, ", ") + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body + "\r\n"
	if _, err := writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func parseMailbox(value string) (*mail.Address, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("address contains invalid characters")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address == "" {
		return nil, fmt.Errorf("invalid email address")
	}
	return address, nil
}

type NoopNotifier struct{}

func (NoopNotifier) Notify(context.Context, string, string) error { return nil }
