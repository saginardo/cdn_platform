package integrations

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCertbotDNSIssuerRetriesMissingTXTRecordOnce(t *testing.T) {
	issuer, statePath := newTestCertbotIssuer(t, "retry-success")
	writeIssuedCertificate(t, issuer.ConfigDir, "site-test")

	waits := 0
	certificate, err := issuer.issue(context.Background(), "site-test", []string{"cdn.example.test"}, func(_ context.Context, delay time.Duration) error {
		waits++
		if delay != 30*time.Second {
			t.Fatalf("retry delay = %s", delay)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(certificate.CertificatePEM) == 0 || waits != 1 {
		t.Fatalf("certificate bytes = %d, waits = %d", len(certificate.CertificatePEM), waits)
	}
	if attempts := readTestFile(t, statePath); attempts != "2" {
		t.Fatalf("certbot attempts = %q", attempts)
	}
	args := strings.Split(strings.TrimSpace(readTestFile(t, statePath+".1.args")), "\n")
	assertArgumentValue(t, args, "--dns-cloudflare-propagation-seconds", "30")
}

func TestCertbotDNSIssuerDoesNotRetryOtherFailures(t *testing.T) {
	issuer, statePath := newTestCertbotIssuer(t, "other-error")
	waits := 0

	_, err := issuer.issue(context.Background(), "site-test", []string{"cdn.example.test"}, func(context.Context, time.Duration) error {
		waits++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "Cloudflare API authentication failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts := readTestFile(t, statePath); attempts != "1" || waits != 0 {
		t.Fatalf("certbot attempts = %q, waits = %d", attempts, waits)
	}
}

func TestCertbotDNSIssuerRetriesMissingTXTRecordAtMostOnce(t *testing.T) {
	issuer, statePath := newTestCertbotIssuer(t, "always-missing")
	waits := 0

	_, err := issuer.issue(context.Background(), "site-test", []string{"cdn.example.test"}, func(context.Context, time.Duration) error {
		waits++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), missingTXTRecordError) {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts := readTestFile(t, statePath); attempts != "2" || waits != 1 {
		t.Fatalf("certbot attempts = %q, waits = %d", attempts, waits)
	}
}

func TestCertbotDNSIssuerStopsWhenRetryWaitIsCanceled(t *testing.T) {
	issuer, statePath := newTestCertbotIssuer(t, "always-missing")
	ctx, cancel := context.WithCancel(context.Background())

	_, err := issuer.issue(ctx, "site-test", []string{"cdn.example.test"}, func(ctx context.Context, delay time.Duration) error {
		cancel()
		return waitForContext(ctx, delay)
	})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts := readTestFile(t, statePath); attempts != "1" {
		t.Fatalf("certbot attempts = %q", attempts)
	}
}

func TestCertbotDNSIssuerDeleteIsIdempotentAndUsesConfiguredDirectories(t *testing.T) {
	issuer, statePath := newTestCertbotIssuer(t, "delete")
	if err := issuer.Delete(context.Background(), "site-missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("Certbot ran for a missing lineage: %v", err)
	}
	renewalDir := filepath.Join(issuer.ConfigDir, "renewal")
	if err := os.MkdirAll(renewalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(renewalDir, "site-existing.conf"), []byte("lineage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := issuer.Delete(context.Background(), "site-existing"); err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(readTestFile(t, statePath+".1.args")), "\n")
	assertArgumentValue(t, args, "--cert-name", "site-existing")
	assertArgumentValue(t, args, "--config-dir", issuer.ConfigDir)
	assertArgumentValue(t, args, "--work-dir", issuer.WorkDir)
	assertArgumentValue(t, args, "--logs-dir", issuer.LogsDir)
}

func newTestCertbotIssuer(t *testing.T, mode string) (CertbotDNSIssuer, string) {
	t.Helper()
	directory := t.TempDir()
	statePath := filepath.Join(directory, "attempts")
	binary := filepath.Join(directory, "certbot")
	script := `#!/bin/sh
attempt=0
if [ -f "$CERTBOT_TEST_STATE" ]; then
  attempt=$(cat "$CERTBOT_TEST_STATE")
fi
attempt=$((attempt + 1))
printf '%s' "$attempt" >"$CERTBOT_TEST_STATE"
printf '%s\n' "$@" >"$CERTBOT_TEST_STATE.$attempt.args"
case "$CERTBOT_TEST_MODE" in
  retry-success)
    if [ "$attempt" -eq 1 ]; then
      echo 'Detail: No TXT record found at _acme-challenge.cdn.example.test' >&2
      exit 1
    fi
    ;;
  always-missing)
    echo 'Detail: No TXT record found at _acme-challenge.cdn.example.test' >&2
    exit 1
    ;;
  other-error)
    echo 'Cloudflare API authentication failed' >&2
    exit 1
    ;;
esac
exit 0
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CERTBOT_TEST_STATE", statePath)
	t.Setenv("CERTBOT_TEST_MODE", mode)
	issuer := CertbotDNSIssuer{
		Binary:    binary,
		Email:     "admin@example.test",
		ConfigDir: filepath.Join(directory, "config"),
		WorkDir:   filepath.Join(directory, "work"),
		LogsDir:   filepath.Join(directory, "logs"),
		Token:     func() (string, error) { return "token", nil },
	}
	return issuer, statePath
}

func writeIssuedCertificate(t *testing.T, configDir, name string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	liveDir := filepath.Join(configDir, "live", name)
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(liveDir, "fullchain.pem"), certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "privkey.pem"), []byte("test-private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertArgumentValue(t *testing.T, args []string, name, expected string) {
	t.Helper()
	for index, argument := range args {
		if argument == name && index+1 < len(args) {
			if args[index+1] != expected {
				t.Fatalf("%s value = %q", name, args[index+1])
			}
			return
		}
	}
	t.Fatalf("argument %s not found in %#v", name, args)
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
