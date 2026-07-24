package control

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReloadingCertificateLoadsReplacementAndKeepsLastValidPair(t *testing.T) {
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "control.crt")
	privateKeyPath := filepath.Join(directory, "control.key")
	writeTestCertificatePair(t, certificatePath, privateKeyPath, 1)
	reloader, err := NewReloadingCertificate(certificatePath, privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := reloader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTestCertificatePair(t, certificatePath, privateKeyPath, 2)
	second, err := reloader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || string(first.Certificate[0]) == string(second.Certificate[0]) {
		t.Fatal("replacement certificate was not loaded")
	}
	if err := os.WriteFile(certificatePath, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback, err := reloader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if fallback != second {
		t.Fatal("invalid replacement did not retain the last valid certificate")
	}
}

func TestReloadingCertificateFollowsCertbotSymlinkReplacement(t *testing.T) {
	directory := t.TempDir()
	archive := filepath.Join(directory, "archive")
	live := filepath.Join(directory, "live")
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestCertificatePair(t, filepath.Join(archive, "cert1.pem"), filepath.Join(archive, "key1.pem"), 1)
	if err := os.Symlink(filepath.Join("..", "archive", "cert1.pem"), filepath.Join(live, "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "archive", "key1.pem"), filepath.Join(live, "privkey.pem")); err != nil {
		t.Fatal(err)
	}
	reloader, err := NewReloadingCertificate(filepath.Join(live, "fullchain.pem"), filepath.Join(live, "privkey.pem"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := reloader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTestCertificatePair(t, filepath.Join(archive, "cert2.pem"), filepath.Join(archive, "key2.pem"), 2)
	if err := os.Remove(filepath.Join(live, "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(live, "privkey.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "archive", "cert2.pem"), filepath.Join(live, "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "archive", "key2.pem"), filepath.Join(live, "privkey.pem")); err != nil {
		t.Fatal(err)
	}
	second, err := reloader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Certificate[0]) == string(second.Certificate[0]) {
		t.Fatal("certificate symlink replacement was not loaded")
	}
}

func TestResolveEdgeBinarySHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge")
	if err := os.WriteFile(path, []byte("edge-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := ResolveEdgeBinarySHA256(path)
	if err != nil || digest != "3eccac6342055b879ba92ce0045aa27c7b2a87d8ef64bea4b82d2b9736c1a764" {
		t.Fatalf("digest = %q, err = %v", digest, err)
	}
	if digest, err := ResolveEdgeBinarySHA256(""); err != nil || digest != "" {
		t.Fatalf("empty path digest = %q, err = %v", digest, err)
	}
}

func writeTestCertificatePair(t *testing.T, certificatePath, privateKeyPath string, serial int64) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "control.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"control.test"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyBytes})
	if err := os.WriteFile(certificatePath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
}
