package integrations

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type IssuedCertificate struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
	NotAfter       time.Time
}

type CertificateIssuer interface {
	Issue(ctx context.Context, name string, domains []string) (IssuedCertificate, error)
}

type CertbotDNSIssuer struct {
	Binary    string
	Email     string
	ConfigDir string
	WorkDir   string
	LogsDir   string
	Token     func() (string, error)
}

func (c CertbotDNSIssuer) Issue(ctx context.Context, name string, domains []string) (IssuedCertificate, error) {
	if len(domains) == 0 {
		return IssuedCertificate{}, fmt.Errorf("at least one domain is required")
	}
	if c.Email == "" {
		return IssuedCertificate{}, fmt.Errorf("ACME email is required")
	}
	token, err := c.Token()
	if err != nil {
		return IssuedCertificate{}, err
	}
	if token == "" {
		return IssuedCertificate{}, fmt.Errorf("Cloudflare API token is empty")
	}
	if c.Binary == "" {
		c.Binary = "certbot"
	}
	configDir := c.ConfigDir
	if configDir == "" {
		configDir = "/var/lib/cdn-platform/letsencrypt"
	}
	workDir := c.WorkDir
	if workDir == "" {
		workDir = "/var/lib/cdn-platform/certbot-work"
	}
	logsDir := c.LogsDir
	if logsDir == "" {
		logsDir = "/var/log/cdn-platform/certbot"
	}
	for _, directory := range []string{configDir, workDir, logsDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return IssuedCertificate{}, err
		}
	}
	credentials, err := os.CreateTemp("", "cdn-platform-cloudflare-*.ini")
	if err != nil {
		return IssuedCertificate{}, err
	}
	credentialsPath := credentials.Name()
	defer os.Remove(credentialsPath)
	if err := credentials.Chmod(0o600); err != nil {
		credentials.Close()
		return IssuedCertificate{}, err
	}
	if _, err := credentials.WriteString("dns_cloudflare_api_token = " + token + "\n"); err != nil {
		credentials.Close()
		return IssuedCertificate{}, err
	}
	if err := credentials.Close(); err != nil {
		return IssuedCertificate{}, err
	}
	args := []string{"certonly", "--non-interactive", "--agree-tos", "--email", c.Email, "--dns-cloudflare", "--dns-cloudflare-credentials", credentialsPath, "--config-dir", configDir, "--work-dir", workDir, "--logs-dir", logsDir, "--cert-name", name}
	for _, domain := range domains {
		args = append(args, "-d", domain)
	}
	command := exec.CommandContext(ctx, c.Binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return IssuedCertificate{}, fmt.Errorf("certbot DNS-01 failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	liveDir := filepath.Join(configDir, "live", name)
	certificate, err := os.ReadFile(filepath.Join(liveDir, "fullchain.pem"))
	if err != nil {
		return IssuedCertificate{}, err
	}
	privateKey, err := os.ReadFile(filepath.Join(liveDir, "privkey.pem"))
	if err != nil {
		return IssuedCertificate{}, err
	}
	block, _ := pem.Decode(certificate)
	if block == nil {
		return IssuedCertificate{}, fmt.Errorf("certbot returned an invalid certificate")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return IssuedCertificate{}, err
	}
	return IssuedCertificate{CertificatePEM: certificate, PrivateKeyPEM: privateKey, NotAfter: parsed.NotAfter}, nil
}
