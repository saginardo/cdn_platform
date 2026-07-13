package control

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type ReloadingCertificate struct {
	certificatePath string
	privateKeyPath  string

	mu          sync.Mutex
	certificate *tls.Certificate
	state       certificateFileState
}

type certificateFileState struct {
	certificateResolvedPath string
	certificateModTime      int64
	certificateSize         int64
	certificateDigest       [sha256.Size]byte
	privateKeyResolvedPath  string
	privateKeyModTime       int64
	privateKeySize          int64
	privateKeyDigest        [sha256.Size]byte
}

func NewReloadingCertificate(certificatePath, privateKeyPath string) (*ReloadingCertificate, error) {
	reloader := &ReloadingCertificate{certificatePath: certificatePath, privateKeyPath: privateKeyPath}
	if _, err := reloader.reload(true); err != nil {
		return nil, err
	}
	return reloader, nil
}

func (r *ReloadingCertificate) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.reload(false)
}

func (r *ReloadingCertificate) reload(required bool) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, err := certificateState(r.certificatePath, r.privateKeyPath)
	if err == nil && r.certificate != nil && state == r.state {
		return r.certificate, nil
	}
	certificate, loadErr := tls.LoadX509KeyPair(r.certificatePath, r.privateKeyPath)
	if loadErr == nil {
		r.certificate = &certificate
		r.state = state
		return r.certificate, nil
	}
	if r.certificate != nil && !required {
		return r.certificate, nil
	}
	return nil, fmt.Errorf("load control TLS certificate: %w", loadErr)
}

func certificateState(certificatePath, privateKeyPath string) (certificateFileState, error) {
	certificateResolvedPath, err := filepath.EvalSymlinks(certificatePath)
	if err != nil {
		return certificateFileState{}, err
	}
	certificateInfo, err := os.Stat(certificatePath)
	if err != nil {
		return certificateFileState{}, err
	}
	privateKeyResolvedPath, err := filepath.EvalSymlinks(privateKeyPath)
	if err != nil {
		return certificateFileState{}, err
	}
	privateKeyInfo, err := os.Stat(privateKeyPath)
	if err != nil {
		return certificateFileState{}, err
	}
	certificateContents, err := os.ReadFile(certificatePath)
	if err != nil {
		return certificateFileState{}, err
	}
	privateKeyContents, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return certificateFileState{}, err
	}
	return certificateFileState{
		certificateResolvedPath: certificateResolvedPath,
		certificateModTime:      certificateInfo.ModTime().UnixNano(),
		certificateSize:         certificateInfo.Size(),
		certificateDigest:       sha256.Sum256(certificateContents),
		privateKeyResolvedPath:  privateKeyResolvedPath,
		privateKeyModTime:       privateKeyInfo.ModTime().UnixNano(),
		privateKeySize:          privateKeyInfo.Size(),
		privateKeyDigest:        sha256.Sum256(privateKeyContents),
	}, nil
}
