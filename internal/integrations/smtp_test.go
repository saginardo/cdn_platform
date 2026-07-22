package integrations

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSMTPNotifierSupportsSTARTTLSAndImplicitTLS(t *testing.T) {
	certificate, roots := smtpTestCertificate(t)
	for _, security := range []string{SMTPSecurityStartTLS, SMTPSecurityTLS} {
		t.Run(security, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			messages := make(chan string, 1)
			errors := make(chan error, 1)
			go func() {
				connection, err := listener.Accept()
				if err != nil {
					errors <- err
					return
				}
				message, err := serveSMTPTestConnection(connection, security, certificate)
				if err != nil {
					errors <- err
					return
				}
				messages <- message
			}()
			port, _ := strconv.Atoi(strings.Split(listener.Addr().String(), ":")[1])
			notifier := SMTPNotifier{
				Host: "localhost", Port: port, Username: "mailer", Password: "secret", From: "cdn@example.test", To: []string{"ops@example.test"}, Security: security,
				TLSConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12},
			}
			if err := notifier.Notify(context.Background(), "Test message", "SMTP body"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-errors:
				t.Fatal(err)
			case message := <-messages:
				if !strings.Contains(message, "Subject: Test message") || !strings.Contains(message, "SMTP body") || !strings.Contains(message, "multipart/alternative") || !strings.Contains(message, "text/html; charset=UTF-8") {
					t.Fatalf("message = %q", message)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("SMTP test server did not finish")
			}
		})
	}
}

func TestNotificationHTMLIsStyledAndEscapesContent(t *testing.T) {
	notification := normalizeNotification(Notification{
		Category:   NotificationCategoryMonitoring,
		Severity:   NotificationSeverityError,
		Subject:    "Probe alert",
		Message:    `<script>alert("unsafe")</script>`,
		Details:    []NotificationDetail{{Label: "Node", Value: `<img src=x onerror=alert(1)>`}},
		OccurredAt: time.Date(2026, 7, 20, 14, 30, 0, 0, time.UTC),
	})
	rendered, err := renderNotificationHTML(notification)
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	for _, expected := range []string{"simple_cdn", "拨测监控", "#b91c1c", "&lt;script&gt;", "&lt;img"} {
		if !strings.Contains(html, expected) {
			t.Fatalf("rendered HTML does not contain %q: %s", expected, html)
		}
	}
	if strings.Contains(html, `<script>`) || strings.Contains(html, `<img src=x`) {
		t.Fatalf("rendered HTML contains unescaped input: %s", html)
	}
}

func TestSMTPNotifierRejectsUnsafeConfiguration(t *testing.T) {
	base := SMTPNotifier{Host: "smtp.example.test", Port: 587, From: "cdn@example.test", To: []string{"ops@example.test"}, Security: SMTPSecurityStartTLS}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := base
	invalid.Security = "none"
	if err := invalid.Validate(); err == nil {
		t.Fatal("accepted plaintext SMTP")
	}
	invalid = base
	invalid.To = []string{"ops@example.test\r\nBcc: hidden@example.test"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("accepted recipient header injection")
	}
}

func serveSMTPTestConnection(connection net.Conn, security string, certificate tls.Certificate) (string, error) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	if security == SMTPSecurityTLS {
		tlsConnection := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
		if err := tlsConnection.Handshake(); err != nil {
			return "", err
		}
		connection = tlsConnection
	}
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	write := func(value string) error {
		if _, err := writer.WriteString(value); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := write("220 localhost ESMTP\r\n"); err != nil {
		return "", err
	}
	tlsActive := security == SMTPSecurityTLS
	var message strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO") || strings.HasPrefix(command, "HELO"):
			if security == SMTPSecurityStartTLS && !tlsActive {
				if err := write("250-localhost\r\n250 STARTTLS\r\n"); err != nil {
					return "", err
				}
			} else if err := write("250-localhost\r\n250 AUTH PLAIN\r\n"); err != nil {
				return "", err
			}
		case strings.HasPrefix(command, "AUTH PLAIN"):
			if !tlsActive {
				return "", fmt.Errorf("received SMTP authentication before TLS")
			}
			if err := write("235 Authentication successful\r\n"); err != nil {
				return "", err
			}
		case command == "STARTTLS":
			if err := write("220 Ready to start TLS\r\n"); err != nil {
				return "", err
			}
			tlsConnection := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			if err := tlsConnection.Handshake(); err != nil {
				return "", err
			}
			connection = tlsConnection
			reader = bufio.NewReader(connection)
			writer = bufio.NewWriter(connection)
			tlsActive = true
		case strings.HasPrefix(command, "MAIL FROM:") || strings.HasPrefix(command, "RCPT TO:"):
			if err := write("250 OK\r\n"); err != nil {
				return "", err
			}
		case command == "DATA":
			if err := write("354 End data with <CR><LF>.<CR><LF>\r\n"); err != nil {
				return "", err
			}
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return "", err
				}
				if dataLine == ".\r\n" {
					break
				}
				message.WriteString(dataLine)
			}
			if err := write("250 Queued\r\n"); err != nil {
				return "", err
			}
		case command == "QUIT":
			if err := write("221 Bye\r\n"); err != nil {
				return "", err
			}
			return message.String(), nil
		default:
			return "", fmt.Errorf("unexpected SMTP command %q", command)
		}
	}
}

func smtpTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certificatePEM)
	return certificate, roots
}
