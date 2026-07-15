package integrations

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
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

type CertificateCleaner interface {
	Delete(ctx context.Context, name string) error
}

const (
	cloudflareDNSPropagationSeconds = 30
	missingTXTRecordRetryDelay      = 30 * time.Second
	missingTXTRecordError           = "No TXT record found"
)

type CertbotDNSIssuer struct {
	Binary    string
	Email     string
	ConfigDir string
	WorkDir   string
	LogsDir   string
	Token     func() (string, error)
}

func (c CertbotDNSIssuer) Issue(ctx context.Context, name string, domains []string) (IssuedCertificate, error) {
	return c.issue(ctx, name, domains, waitForContext)
}

func (c CertbotDNSIssuer) Delete(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("invalid Certbot certificate name")
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
	_, renewalErr := os.Stat(filepath.Join(configDir, "renewal", name+".conf"))
	_, liveErr := os.Stat(filepath.Join(configDir, "live", name))
	if errors.Is(renewalErr, os.ErrNotExist) && errors.Is(liveErr, os.ErrNotExist) {
		return nil
	}
	if renewalErr != nil && !errors.Is(renewalErr, os.ErrNotExist) {
		return renewalErr
	}
	if liveErr != nil && !errors.Is(liveErr, os.ErrNotExist) {
		return liveErr
	}
	binary := c.Binary
	if binary == "" {
		binary = "certbot"
	}
	for _, directory := range []string{configDir, workDir, logsDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
	}
	output, err := runCertbot(ctx, binary, []string{"delete", "--non-interactive", "--cert-name", name,
		"--config-dir", configDir, "--work-dir", workDir, "--logs-dir", logsDir})
	if err != nil {
		return fmt.Errorf("certbot delete failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (c CertbotDNSIssuer) issue(ctx context.Context, name string, domains []string, wait func(context.Context, time.Duration) error) (IssuedCertificate, error) {
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
	args := []string{"certonly", "--non-interactive", "--agree-tos", "--email", c.Email, "--dns-cloudflare", "--dns-cloudflare-credentials", credentialsPath, "--dns-cloudflare-propagation-seconds", fmt.Sprintf("%d", cloudflareDNSPropagationSeconds), "--config-dir", configDir, "--work-dir", workDir, "--logs-dir", logsDir, "--cert-name", name}
	for _, domain := range domains {
		args = append(args, "-d", domain)
	}
	output, err := runCertbot(ctx, c.Binary, args)
	if err != nil && strings.Contains(string(output), missingTXTRecordError) {
		if waitErr := wait(ctx, missingTXTRecordRetryDelay); waitErr != nil {
			return IssuedCertificate{}, fmt.Errorf("waiting to retry certbot DNS-01 after missing TXT record: %w", waitErr)
		}
		output, err = runCertbot(ctx, c.Binary, args)
	}
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

func runCertbot(ctx context.Context, binary string, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, binary, args...).CombinedOutput()
}

func waitForContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
