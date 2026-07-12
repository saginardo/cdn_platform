package control

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

type InternalCA struct {
	Certificate    *x509.Certificate
	Signer         crypto.Signer
	CertificatePEM []byte
}

func LoadOrCreateInternalCA(directory string) (*InternalCA, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	certificatePath := filepath.Join(directory, "edge-ca.crt")
	keyPath := filepath.Join(directory, "edge-ca.key")
	certificatePEM, certificateErr := os.ReadFile(certificatePath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certificateErr == nil && keyErr == nil {
		return loadCA(certificatePEM, keyPEM)
	}
	if !errors.Is(certificateErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read internal CA: certificate=%v key=%v", certificateErr, keyErr)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "cdn-platform edge internal CA"},
		NotBefore:    now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certificatePEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writePrivateFile(certificatePath, certificatePEM, 0o644); err != nil {
		return nil, err
	}
	if err := writePrivateFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	return loadCA(certificatePEM, keyPEM)
}

func loadCA(certificatePEM, keyPEM []byte) (*InternalCA, error) {
	certificateBlock, _ := pem.Decode(certificatePEM)
	if certificateBlock == nil {
		return nil, errors.New("invalid internal CA certificate")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("invalid internal CA key")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := parsedKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("internal CA key is not a signer")
	}
	return &InternalCA{Certificate: certificate, Signer: signer, CertificatePEM: certificatePEM}, nil
}

func (ca *InternalCA) SignCSR(csrPEM []byte, nodeID string) ([]byte, error) {
	csr, err := ParseAndVerifyCSR(csrPEM)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "cdn-edge:" + nodeID},
		NotBefore:    now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.Certificate, csr.PublicKey, ca.Signer)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func (ca *InternalCA) SignRenewal(certificateDER []byte, csrPEM []byte, nodeID string) ([]byte, error) {
	certificate, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		return nil, errors.New("invalid existing edge certificate")
	}
	if certificate.CheckSignatureFrom(ca.Certificate) != nil || certificate.Subject.CommonName != "cdn-edge:"+nodeID {
		return nil, errors.New("edge certificate is not eligible for renewal")
	}
	return ca.SignCSR(csrPEM, nodeID)
}

func ParseAndVerifyCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("invalid certificate signing request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR signature: %w", err)
	}
	return csr, nil
}

func CertificateFingerprintPEM(certificatePEM []byte) (string, error) {
	block, _ := pem.Decode(certificatePEM)
	if block == nil {
		return "", errors.New("invalid certificate PEM")
	}
	return CertificateFingerprintDER(block.Bytes), nil
}

func CertificateFingerprintDER(certificateDER []byte) string {
	sum := sha256.Sum256(certificateDER)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func writePrivateFile(path string, contents []byte, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, contents, mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
