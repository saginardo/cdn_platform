package edge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cdn-platform/internal/domain"
)

type Config struct {
	ControlURL      string
	EnrollmentToken string
	StateDir        string
	NginxConfigPath string
	CertificateDir  string
	ClientKeyPath   string
	ClientCertPath  string
	CAPath          string
	AccessLogPath   string
	PollInterval    time.Duration
	HTTPClient      *http.Client
	Runner          Runner
}

type Agent struct {
	Config          Config
	logs            *LogForwarder
	lastApplyReport *domain.ApplyReport
}

func New(config Config) (*Agent, error) {
	parsedControlURL, err := url.Parse(strings.TrimSpace(config.ControlURL))
	if err != nil || parsedControlURL.Scheme != "https" || parsedControlURL.Host == "" || parsedControlURL.User != nil || parsedControlURL.Fragment != "" {
		return nil, errors.New("CONTROL_URL must be an absolute HTTPS URL")
	}
	config.ControlURL = strings.TrimRight(config.ControlURL, "/")
	if config.StateDir == "" {
		config.StateDir = "/opt/cdn-edge/data"
	}
	if config.NginxConfigPath == "" {
		config.NginxConfigPath = "/opt/cdn-edge/config/nginx/cdn-platform.conf"
	}
	if config.CertificateDir == "" {
		config.CertificateDir = "/opt/cdn-edge/config/certs"
	}
	if config.ClientKeyPath == "" {
		config.ClientKeyPath = filepath.Join(config.StateDir, "edge-client.key")
	}
	if config.ClientCertPath == "" {
		config.ClientCertPath = filepath.Join(config.StateDir, "edge-client.crt")
	}
	if config.CAPath == "" {
		config.CAPath = filepath.Join(config.StateDir, "edge-ca.crt")
	}
	if config.AccessLogPath == "" {
		config.AccessLogPath = "/opt/cdn-edge/logs/access.json"
	}
	if config.PollInterval == 0 {
		config.PollInterval = 30 * time.Second
	}
	if config.Runner == nil {
		config.Runner = NginxRunner{}
	}
	if err := os.MkdirAll(config.StateDir, 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(config.CertificateDir, 0o700); err != nil {
		return nil, err
	}
	return &Agent{Config: config, logs: NewLogForwarder(config.StateDir, config.AccessLogPath)}, nil
}

func (a *Agent) Run(ctx context.Context) error {
	if err := a.EnsureEnrollment(ctx); err != nil {
		return err
	}
	for {
		lastError := ""
		if err := a.renewIfNeeded(ctx); err != nil {
			lastError = "renew edge certificate: " + err.Error()
		}
		if err := a.Sync(ctx); err != nil && lastError == "" {
			lastError = err.Error()
		}
		if _, err := a.logs.Collect(); err != nil && lastError == "" {
			lastError = "collect access logs: " + err.Error()
		}
		if err := a.logs.Flush(ctx, a.Config.ControlURL, a.client()); err != nil && lastError == "" {
			lastError = "upload access logs: " + err.Error()
		}
		if err := a.Heartbeat(ctx, a.appliedVersion(), lastError, a.lastApplyReport); err == nil {
			a.lastApplyReport = nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.Config.PollInterval):
		}
	}
}

func (a *Agent) EnsureEnrollment(ctx context.Context) error {
	if pathExists(a.Config.ClientCertPath) && pathExists(a.Config.ClientKeyPath) && pathExists(a.Config.CAPath) {
		return a.renewIfNeeded(ctx)
	}
	if strings.TrimSpace(a.Config.EnrollmentToken) == "" {
		return errors.New("edge is not enrolled and ENROLLMENT_TOKEN is empty")
	}
	_, csr, err := a.loadOrCreateKeyAndCSR()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"enrollment_token": a.Config.EnrollmentToken, "csr": string(csr)})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Config.ControlURL+"/api/edge/v1/enroll", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.bootstrapClient().Do(request)
	if err != nil {
		return fmt.Errorf("enroll edge: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("enroll edge: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		ClientCertificate string `json:"client_certificate"`
		CACertificate     string `json:"ca_certificate"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if result.ClientCertificate == "" || result.CACertificate == "" {
		return errors.New("enrollment response is missing certificates")
	}
	if err := atomicWriteFile(a.Config.ClientCertPath, []byte(result.ClientCertificate), 0o600); err != nil {
		return err
	}
	if err := atomicWriteFile(a.Config.CAPath, []byte(result.CACertificate), 0o644); err != nil {
		return err
	}
	return nil
}

func (a *Agent) renewIfNeeded(ctx context.Context) error {
	certificatePEM, err := os.ReadFile(a.Config.ClientCertPath)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil {
		return errors.New("invalid edge client certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if certificate.NotAfter.After(time.Now().UTC().Add(30 * 24 * time.Hour)) {
		return nil
	}
	_, csr, err := a.loadOrCreateKeyAndCSR()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"csr": string(csr)})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Config.ControlURL+"/api/edge/v1/renew", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client().Do(request)
	if err != nil {
		return fmt.Errorf("renew edge certificate: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("renew edge certificate: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		ClientCertificate string `json:"client_certificate"`
		CACertificate     string `json:"ca_certificate"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if result.ClientCertificate == "" || result.CACertificate == "" {
		return errors.New("renewal response is missing certificates")
	}
	if err := atomicWriteFile(a.Config.ClientCertPath, []byte(result.ClientCertificate), 0o600); err != nil {
		return err
	}
	return atomicWriteFile(a.Config.CAPath, []byte(result.CACertificate), 0o644)
}

func (a *Agent) Sync(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Config.ControlURL+"/api/edge/v1/desired-state", nil)
	if err != nil {
		return err
	}
	response, err := a.client().Do(request)
	if err != nil {
		return fmt.Errorf("pull desired state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("pull desired state: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var state domain.DesiredState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		return err
	}
	if state.Version == 0 || state.NginxConfig == "" || state.Version <= a.appliedVersion() {
		return nil
	}
	return a.apply(state)
}

func (a *Agent) Heartbeat(ctx context.Context, appliedVersion int64, lastError string, report *domain.ApplyReport) error {
	payload, err := json.Marshal(map[string]any{"last_error": lastError, "applied_version": appliedVersion, "apply_report": report})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Config.ControlURL+"/api/edge/v1/heartbeat", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat: %s", response.Status)
	}
	return nil
}

func (a *Agent) apply(state domain.DesiredState) error {
	ports := desiredPublicPorts(state)
	listeners, err := a.Config.Runner.PortListeners(ports)
	if err != nil {
		return a.applyFailed(state.Version, "port_check_failed", err, nil)
	}
	if conflicts := foreignListeners(listeners); len(conflicts) > 0 {
		return a.applyFailed(state.Version, "port_conflict", fmt.Errorf("public port is already held by another service: %s", formatPortConflicts(conflicts)), conflicts)
	}
	backups := make(map[string]fileBackup)
	for siteID, certificate := range state.Certificates {
		certificatePath := filepath.Join(a.Config.CertificateDir, siteID+".crt")
		privateKeyPath := filepath.Join(a.Config.CertificateDir, siteID+".key")
		for _, path := range []string{certificatePath, privateKeyPath} {
			backup, err := readBackup(path)
			if err != nil {
				return a.applyFailed(state.Version, "certificate_backup_failed", err, nil)
			}
			backups[path] = backup
		}
		if err := atomicWriteFile(certificatePath, []byte(certificate.CertificatePEM), 0o600); err != nil {
			return a.applyFailed(state.Version, "certificate_write_failed", fmt.Errorf("write certificate for %s: %w", siteID, err), nil)
		}
		if err := atomicWriteFile(privateKeyPath, []byte(certificate.PrivateKeyPEM), 0o600); err != nil {
			restoreBackups(backups)
			return a.applyFailed(state.Version, "private_key_write_failed", fmt.Errorf("write private key for %s: %w", siteID, err), nil)
		}
	}
	previous, previousErr := os.ReadFile(a.Config.NginxConfigPath)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return a.applyFailed(state.Version, "config_backup_failed", previousErr, nil)
	}
	if err := atomicWriteFile(a.Config.NginxConfigPath, []byte(state.NginxConfig), 0o640); err != nil {
		restoreBackups(backups)
		return a.applyFailed(state.Version, "config_write_failed", fmt.Errorf("write Nginx configuration: %w", err), nil)
	}
	if err := a.Config.Runner.Test(); err != nil {
		a.restorePreviousConfig(previous, previousErr)
		restoreBackups(backups)
		return a.applyFailed(state.Version, "nginx_config_invalid", err, nil)
	}
	if err := a.Config.Runner.Apply(); err != nil {
		a.restorePreviousConfig(previous, previousErr)
		restoreBackups(backups)
		if listeners, inspectErr := a.Config.Runner.PortListeners(ports); inspectErr == nil {
			if conflicts := foreignListeners(listeners); len(conflicts) > 0 {
				return a.applyFailed(state.Version, "port_conflict", fmt.Errorf("public port is already held by another service: %s", formatPortConflicts(conflicts)), conflicts)
			}
		}
		return a.applyFailed(state.Version, "nginx_apply_failed", err, nil)
	}
	listeners, err = a.Config.Runner.PortListeners(ports)
	if err != nil {
		return a.applyFailed(state.Version, "port_check_failed", err, nil)
	}
	if conflicts := foreignListeners(listeners); len(conflicts) > 0 {
		a.restorePreviousConfig(previous, previousErr)
		restoreBackups(backups)
		return a.applyFailed(state.Version, "port_conflict", fmt.Errorf("public port is already held by another service: %s", formatPortConflicts(conflicts)), conflicts)
	}
	if !nginxOwnsPorts(listeners, ports) {
		a.restorePreviousConfig(previous, previousErr)
		restoreBackups(backups)
		return a.applyFailed(state.Version, "nginx_not_listening", fmt.Errorf("Nginx did not retain all required public listeners after applying configuration: %s", formatPorts(ports)), nil)
	}
	if err := atomicWriteFile(filepath.Join(a.Config.StateDir, "applied-version"), []byte(fmt.Sprintf("%d\n", state.Version)), 0o640); err != nil {
		return a.applyFailed(state.Version, "applied_version_write_failed", err, nil)
	}
	a.lastApplyReport = &domain.ApplyReport{Version: state.Version, Status: domain.ApplySucceeded, Detail: "Nginx is listening on " + formatPorts(ports)}
	return nil
}

func desiredPublicPorts(state domain.DesiredState) []int {
	if len(state.PublicPorts) == 0 {
		// Desired states emitted before this field was introduced all represented
		// a public CDN configuration and therefore retain the original 80/443
		// expectation during a rolling control-plane upgrade.
		return []int{80, 443}
	}
	ports := make([]int, 0, len(state.PublicPorts))
	seen := make(map[int]bool, len(state.PublicPorts))
	for _, port := range state.PublicPorts {
		if (port != 80 && port != 443) || seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return []int{80, 443}
	}
	return ports
}

func (a *Agent) restorePreviousConfig(previous []byte, previousErr error) {
	if previousErr == nil {
		if err := atomicWriteFile(a.Config.NginxConfigPath, previous, 0o640); err == nil {
			if a.Config.Runner.Test() == nil {
				_ = a.Config.Runner.Apply()
			}
		}
		return
	}
	_ = os.Remove(a.Config.NginxConfigPath)
}

func (a *Agent) applyFailed(version int64, code string, err error, conflicts []domain.PortConflict) error {
	a.lastApplyReport = &domain.ApplyReport{Version: version, Status: domain.ApplyFailed, Code: code, Detail: err.Error(), PortConflicts: conflicts}
	return err
}

func foreignListeners(listeners []domain.PortConflict) []domain.PortConflict {
	conflicts := make([]domain.PortConflict, 0)
	for _, listener := range listeners {
		name := strings.ToLower(strings.TrimSpace(listener.Process))
		if name == "nginx" || strings.HasPrefix(name, "nginx:") {
			continue
		}
		conflicts = append(conflicts, listener)
	}
	return conflicts
}

func nginxOwnsPorts(listeners []domain.PortConflict, ports []int) bool {
	owned := make(map[int]bool, len(ports))
	for _, listener := range listeners {
		name := strings.ToLower(strings.TrimSpace(listener.Process))
		if name == "nginx" || strings.HasPrefix(name, "nginx:") {
			owned[listener.Port] = true
		}
	}
	for _, port := range ports {
		if !owned[port] {
			return false
		}
	}
	return true
}

func formatPortConflicts(conflicts []domain.PortConflict) string {
	parts := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		value := fmt.Sprintf("TCP %d: %s", conflict.Port, conflict.Process)
		if conflict.PID > 0 {
			value += fmt.Sprintf(" (PID %d)", conflict.PID)
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, ", ")
}

func formatPorts(ports []int) string {
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, fmt.Sprintf("TCP %d", port))
	}
	return strings.Join(values, " and ")
}

type fileBackup struct {
	contents []byte
	exists   bool
}

func readBackup(path string) (fileBackup, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileBackup{}, nil
	}
	if err != nil {
		return fileBackup{}, err
	}
	return fileBackup{contents: contents, exists: true}, nil
}

func restoreBackups(backups map[string]fileBackup) {
	for path, backup := range backups {
		if backup.exists {
			_ = atomicWriteFile(path, backup.contents, 0o600)
		} else {
			_ = os.Remove(path)
		}
	}
}

func (a *Agent) loadOrCreateKeyAndCSR() (*ecdsa.PrivateKey, []byte, error) {
	var key *ecdsa.PrivateKey
	if existing, err := os.ReadFile(a.Config.ClientKeyPath); err == nil {
		block, _ := pem.Decode(existing)
		if block == nil {
			return nil, nil, errors.New("invalid edge client key")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, nil, err
		}
		var ok bool
		key, ok = parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, nil, errors.New("edge client key is not ECDSA")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, nil, err
		}
		if err := atomicWriteFile(a.Config.ClientKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, nil, err
		}
	} else {
		return nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkixName("cdn-edge")}, key)
	if err != nil {
		return nil, nil, err
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), nil
}

func (a *Agent) bootstrapClient() *http.Client {
	if a.Config.HTTPClient != nil {
		return a.Config.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (a *Agent) client() *http.Client {
	if a.Config.HTTPClient != nil {
		return a.Config.HTTPClient
	}
	certificate, err := tls.LoadX509KeyPair(a.Config.ClientCertPath, a.Config.ClientKeyPath)
	if err != nil {
		return &http.Client{Timeout: 30 * time.Second, Transport: rejectingTransport{err: err}}
	}
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if internalCA, err := os.ReadFile(a.Config.CAPath); err == nil {
		roots.AppendCertsFromPEM(internalCA)
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, RootCAs: roots}}}
}

func (a *Agent) appliedVersion() int64 {
	contents, err := os.ReadFile(filepath.Join(a.Config.StateDir, "applied-version"))
	if err != nil {
		return 0
	}
	var version int64
	_, _ = fmt.Sscanf(strings.TrimSpace(string(contents)), "%d", &version)
	return version
}

type rejectingTransport struct{ err error }

func (r rejectingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }

func pkixName(commonName string) pkix.Name { return pkix.Name{CommonName: commonName} }

func atomicWriteFile(path string, contents []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".cdn-platform-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func pathExists(path string) bool { _, err := os.Stat(path); return err == nil }
