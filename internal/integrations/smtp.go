package integrations

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

type Notifier interface {
	Notify(ctx context.Context, subject, body string) error
}

type SMTPNotifier struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
	To       []string
	StartTLS bool
}

func (s SMTPNotifier) Notify(ctx context.Context, subject, body string) error {
	if len(s.To) == 0 {
		return nil
	}
	if s.Host == "" || s.Port == "" || s.From == "" {
		return fmt.Errorf("SMTP host, port, and from address are required")
	}
	address := net.JoinHostPort(s.Host, s.Port)
	connection, err := net.Dial("tcp", address)
	if err != nil {
		return err
	}
	defer connection.Close()
	client, err := smtp.NewClient(connection, s.Host)
	if err != nil {
		return err
	}
	defer client.Quit()
	if s.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(s.From); err != nil {
		return err
	}
	for _, recipient := range s.To {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := "From: " + s.From + "\r\nTo: " + strings.Join(s.To, ", ") + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body + "\r\n"
	if _, err := writer.Write([]byte(message)); err != nil {
		writer.Close()
		return err
	}
	return writer.Close()
}

type NoopNotifier struct{}

func (NoopNotifier) Notify(context.Context, string, string) error { return nil }
