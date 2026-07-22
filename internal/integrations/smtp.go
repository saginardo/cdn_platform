package integrations

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
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
	return s.NotifyNotification(ctx, Notification{
		Category: NotificationCategoryAvailability,
		Severity: NotificationSeverityInfo,
		Subject:  subject,
		Message:  body,
	})
}

func (s SMTPNotifier) NotifyNotification(ctx context.Context, notification Notification) error {
	if len(s.To) == 0 {
		return nil
	}
	if err := s.Validate(); err != nil {
		return err
	}
	notification = normalizeNotification(notification)
	if notification.Subject == "" {
		return fmt.Errorf("SMTP subject is required")
	}
	if strings.ContainsAny(notification.Subject, "\r\n") {
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
	message, err := buildNotificationMessage(from.String(), toHeader, notification)
	if err != nil {
		_ = writer.Close()
		return err
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

type notificationEmailView struct {
	Subject       string
	Message       string
	CategoryLabel string
	SeverityLabel string
	Accent        string
	Tint          string
	Details       []NotificationDetail
	OccurredAt    string
}

var notificationEmailTemplate = template.Must(template.New("notification-email").Parse(`<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;background:#f3f4f6;color:#111827;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',Arial,sans-serif;">
  <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="border-collapse:collapse;background:#f3f4f6;">
    <tr><td align="center" style="padding:28px 12px;">
      <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:640px;border-collapse:separate;background:#ffffff;border:1px solid #e5e7eb;border-top:4px solid {{.Accent}};">
        <tr><td style="padding:24px 28px 16px;">
          <table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr>
            <td style="font-size:12px;font-weight:700;color:#4b5563;text-transform:uppercase;">simple_cdn</td>
            <td align="right"><span style="display:inline-block;padding:4px 8px;background:{{.Tint}};color:{{.Accent}};font-size:12px;font-weight:700;">{{.CategoryLabel}} · {{.SeverityLabel}}</span></td>
          </tr></table>
          <h1 style="margin:18px 0 10px;font-size:21px;line-height:1.4;font-weight:700;letter-spacing:0;color:#111827;">{{.Subject}}</h1>
          {{if .Message}}<p style="margin:0;font-size:14px;line-height:1.75;color:#374151;white-space:pre-line;">{{.Message}}</p>{{end}}
        </td></tr>
        {{if .Details}}<tr><td style="padding:0 28px 20px;">
          <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="border-collapse:collapse;border:1px solid #e5e7eb;">
            {{range .Details}}<tr>
              <td width="34%" style="padding:10px 12px;border-bottom:1px solid #e5e7eb;background:#f9fafb;font-size:13px;font-weight:600;color:#4b5563;vertical-align:top;">{{.Label}}</td>
              <td style="padding:10px 12px;border-bottom:1px solid #e5e7eb;font-size:13px;line-height:1.55;color:#111827;word-break:break-word;">{{.Value}}</td>
            </tr>{{end}}
          </table>
        </td></tr>{{end}}
        <tr><td style="padding:14px 28px;border-top:1px solid #e5e7eb;background:#f9fafb;font-size:12px;color:#6b7280;">
          发生时间：{{.OccurredAt}} · 此邮件由控制面自动发送
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`))

func buildNotificationMessage(from string, recipients []string, notification Notification) ([]byte, error) {
	notification = normalizeNotification(notification)
	plainText := notification.PlainText()
	htmlBody, err := renderNotificationHTML(notification)
	if err != nil {
		return nil, err
	}
	var multipartBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&multipartBody)
	plainHeader := textproto.MIMEHeader{}
	plainHeader.Set("Content-Type", "text/plain; charset=UTF-8")
	plainHeader.Set("Content-Transfer-Encoding", "8bit")
	plainPart, err := multipartWriter.CreatePart(plainHeader)
	if err != nil {
		return nil, err
	}
	if _, err := plainPart.Write([]byte(plainText + "\r\n")); err != nil {
		return nil, err
	}
	htmlHeader := textproto.MIMEHeader{}
	htmlHeader.Set("Content-Type", "text/html; charset=UTF-8")
	htmlHeader.Set("Content-Transfer-Encoding", "8bit")
	htmlPart, err := multipartWriter.CreatePart(htmlHeader)
	if err != nil {
		return nil, err
	}
	if _, err := htmlPart.Write(htmlBody); err != nil {
		return nil, err
	}
	if err := multipartWriter.Close(); err != nil {
		return nil, err
	}
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\n", from)
	fmt.Fprintf(&message, "To: %s\r\n", strings.Join(recipients, ", "))
	fmt.Fprintf(&message, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", notification.Subject))
	fmt.Fprintf(&message, "Date: %s\r\n", notification.OccurredAt.Format(time.RFC1123Z))
	fmt.Fprint(&message, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&message, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", multipartWriter.Boundary())
	message.Write(multipartBody.Bytes())
	return message.Bytes(), nil
}

func renderNotificationHTML(notification Notification) ([]byte, error) {
	accent, tint, severityLabel := notificationSeverityStyle(notification.Severity)
	view := notificationEmailView{
		Subject: notification.Subject, Message: notification.Message,
		CategoryLabel: notificationCategoryLabel(notification.Category),
		SeverityLabel: severityLabel, Accent: accent, Tint: tint,
		Details: notification.Details, OccurredAt: formatNotificationTime(notification.OccurredAt),
	}
	var rendered bytes.Buffer
	if err := notificationEmailTemplate.Execute(&rendered, view); err != nil {
		return nil, err
	}
	return rendered.Bytes(), nil
}

func notificationCategoryLabel(category NotificationCategory) string {
	switch category {
	case NotificationCategoryMonitoring:
		return "拨测监控"
	case NotificationCategoryCertificate:
		return "证书续期"
	case NotificationCategoryBackup:
		return "备份任务"
	default:
		return "可用性"
	}
}

func notificationSeverityStyle(severity NotificationSeverity) (accent, tint, label string) {
	switch severity {
	case NotificationSeveritySuccess:
		return "#047857", "#ecfdf5", "已恢复"
	case NotificationSeverityWarning:
		return "#b45309", "#fffbeb", "警告"
	case NotificationSeverityError:
		return "#b91c1c", "#fef2f2", "异常"
	default:
		return "#1d4ed8", "#eff6ff", "通知"
	}
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

func (NoopNotifier) NotifyNotification(context.Context, Notification) error { return nil }
