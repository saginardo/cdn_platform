package control

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

func TestPublishRequiresCertificateAndThenMarksPublished(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, err := NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{Name: "site", Domains: []string{"cdn.example.test"}, Nodes: []string{node.ID}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	publisher := Publisher{Store: database, Cipher: cipher}
	if _, err := publisher.PublishSite(site.ID); err == nil {
		t.Fatal("expected certificate gate")
	}
	certificate, privateKey, notAfter := testCertificate(t, "cdn.example.test")
	if err := publisher.StoreCertificate(site.ID, certificate, privateKey, notAfter); err != nil {
		t.Fatal(err)
	}
	task, err := publisher.PublishSite(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskSucceeded || !strings.Contains(task.Detail, "no active") {
		t.Fatalf("unexpected task: %#v", task)
	}
	published, _, err := database.GetSite(site.ID)
	if err != nil || !published.Published {
		t.Fatalf("site did not become published: %#v %v", published, err)
	}
	state, _, err := database.NodeState(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 || state.NginxConfig == "" {
		t.Fatalf("unexpected node state: %#v", state)
	}
	if _, _, _, err := database.Certificate("missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected missing certificate: %v", err)
	}
}

func TestPublishWaitsForActiveEdgeConfirmation(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, err := NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(node.ID, 0, "", nil); err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{Name: "site", Domains: []string{"cdn.example.test"}, Nodes: []string{node.ID}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	publisher := Publisher{Store: database, Cipher: cipher}
	certificate, privateKey, notAfter := testCertificate(t, "cdn.example.test")
	if err := publisher.StoreCertificate(site.ID, certificate, privateKey, notAfter); err != nil {
		t.Fatal(err)
	}
	task, err := publisher.PublishSite(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskApplying || task.DeadlineAt == nil {
		t.Fatalf("publish should wait for edge: %#v", task)
	}
	state, _, err := database.NodeState(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(node.ID, state.Version, "", &domain.ApplyReport{Version: state.Version, Status: domain.ApplySucceeded, Detail: "Nginx is listening"}); err != nil {
		t.Fatal(err)
	}
	status, err := database.PublishStatus(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Task == nil || status.Task.Status != domain.TaskSucceeded || len(status.Nodes) != 1 || status.Nodes[0].Status != domain.PublishNodeSucceeded || status.Nodes[0].Detail != "Nginx is listening" {
		t.Fatalf("unexpected published status: %#v", status)
	}
}

func TestPublishRecordsPortConflictFromEdge(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	task, created, err := database.CreateOrGetActivePublishTask("site", time.Now().Add(time.Minute))
	if err != nil || !created {
		t.Fatalf("create task: %#v %t %v", task, created, err)
	}
	if err := database.UpdateTask(task.ID, domain.TaskApplying, "waiting for edge"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreatePublishTaskNodes(task.ID, []store.PublishTaskNode{{NodeID: node.ID, TargetVersion: 5}}); err != nil {
		t.Fatal(err)
	}
	report := &domain.ApplyReport{Version: 5, Status: domain.ApplyFailed, Code: "port_conflict", Detail: "TCP 80 is in use", PortConflicts: []domain.PortConflict{{Port: 80, PID: 1234, Process: "caddy"}}}
	if err := database.Heartbeat(node.ID, 0, report.Detail, report); err != nil {
		t.Fatal(err)
	}
	status, err := database.PublishStatus("site")
	if err != nil {
		t.Fatal(err)
	}
	if status.Task == nil || status.Task.Status != domain.TaskFailed || len(status.Nodes) != 1 || status.Nodes[0].ErrorCode != "port_conflict" || len(status.Nodes[0].PortConflicts) != 1 {
		t.Fatalf("unexpected conflict status: %#v", status)
	}
}

func testCertificate(t *testing.T, domains ...string) ([]byte, []byte, time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}), certificate.NotAfter
}

func TestLoginAndEnrollmentCommandGuard(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, err := NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database, Cipher: cipher, ControlURL: "https://control.example.test", EdgeControlURL: "https://edge-control.example.test:8443"}
	setup := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewBufferString(`{"password":"correct horse battery staple","totp_secret":"JBSWY3DPEHPK3PXP"}`))
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	login := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString(`{"password":"correct horse battery staple","recovery_code":"`+decodeRecoveryCode(t, setupResponse.Body.Bytes())+`"}`))
	login.Header.Set("Content-Type", "application/json")
	login.RemoteAddr = "127.0.0.1:12345"
	loginResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	sessionRequest.AddCookie(cookie)
	sessionResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK || decodeCSRF(t, sessionResponse.Body.Bytes()) != decodeCSRF(t, loginResponse.Body.Bytes()) {
		t.Fatalf("session must return the existing CSRF token: %d %s", sessionResponse.Code, sessionResponse.Body.String())
	}
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	guarded := httptest.NewRequest(http.MethodPost, "/api/nodes/"+node.ID+"/enrollment-token", nil)
	guarded.AddCookie(cookie)
	guarded.Header.Set("X-CSRF-Token", decodeCSRF(t, loginResponse.Body.Bytes()))
	guardedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(guardedResponse, guarded)
	if guardedResponse.Code != http.StatusConflict {
		t.Fatalf("expected EdgeBinaryURL guard, got %d %s", guardedResponse.Code, guardedResponse.Body.String())
	}
	server.EdgeBinaryURL = "https://downloads.example.test/edge"
	server.EdgeBinarySHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ready := httptest.NewRequest(http.MethodPost, "/api/nodes/"+node.ID+"/enrollment-token", nil)
	ready.AddCookie(cookie)
	ready.Header.Set("X-CSRF-Token", decodeCSRF(t, loginResponse.Body.Bytes()))
	readyResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(readyResponse, ready)
	if readyResponse.Code != http.StatusCreated || !bytes.Contains(readyResponse.Body.Bytes(), []byte("--binary-sha256")) || !bytes.Contains(readyResponse.Body.Bytes(), []byte("sudo bash -s")) || !bytes.Contains(readyResponse.Body.Bytes(), []byte("https://edge-control.example.test:8443")) {
		t.Fatalf("expected checksum-bound command, got %d %s", readyResponse.Code, readyResponse.Body.String())
	}
	server.EdgeBinarySHA256 = strings.Repeat("z", 64)
	invalid := httptest.NewRequest(http.MethodPost, "/api/nodes/"+node.ID+"/enrollment-token", nil)
	invalid.AddCookie(cookie)
	invalid.Header.Set("X-CSRF-Token", decodeCSRF(t, loginResponse.Body.Bytes()))
	invalidResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusConflict {
		t.Fatalf("expected malformed checksum guard, got %d %s", invalidResponse.Code, invalidResponse.Body.String())
	}
	server.EdgeBinarySHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	server.EdgeBinaryURL = "http://downloads.example.test/edge"
	insecure := httptest.NewRequest(http.MethodPost, "/api/nodes/"+node.ID+"/enrollment-token", nil)
	insecure.AddCookie(cookie)
	insecure.Header.Set("X-CSRF-Token", decodeCSRF(t, loginResponse.Body.Bytes()))
	insecureResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(insecureResponse, insecure)
	if insecureResponse.Code != http.StatusConflict {
		t.Fatalf("expected HTTP binary URL guard, got %d %s", insecureResponse.Code, insecureResponse.Body.String())
	}
}

func TestRequestIPHonorsTrustedProxyOnly(t *testing.T) {
	_, trustedProxy, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{TrustedProxyCIDRs: []*net.IPNet{trustedProxy}}
	proxied := httptest.NewRequest(http.MethodGet, "/", nil)
	proxied.RemoteAddr = "127.0.0.1:49152"
	proxied.Header.Set("X-Real-IP", "203.0.113.10")
	if got := server.requestIP(proxied); got != "203.0.113.10" {
		t.Fatalf("trusted proxy address = %q", got)
	}
	direct := httptest.NewRequest(http.MethodGet, "/", nil)
	direct.RemoteAddr = "198.51.100.11:49152"
	direct.Header.Set("X-Real-IP", "203.0.113.10")
	if got := server.requestIP(direct); got != "198.51.100.11" {
		t.Fatalf("direct client address = %q", got)
	}
}

func TestEdgeBinaryRequiresConfiguredRegularFile(t *testing.T) {
	server := &Server{}
	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/downloads/cdn-edge-agent-linux-amd64", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing edge binary = %d", missing.Code)
	}
	path := filepath.Join(t.TempDir(), "cdn-edge-agent-linux-amd64")
	if err := os.WriteFile(path, []byte("edge-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.EdgeBinaryPath = path
	served := httptest.NewRecorder()
	server.Handler().ServeHTTP(served, httptest.NewRequest(http.MethodGet, "/downloads/cdn-edge-agent-linux-amd64", nil))
	if served.Code != http.StatusOK || served.Body.String() != "edge-binary" {
		t.Fatalf("edge binary response = %d %q", served.Code, served.Body.String())
	}
	if got := served.Header().Get("Content-Disposition"); !strings.Contains(got, "cdn-edge-agent-linux-amd64") {
		t.Fatalf("content disposition = %q", got)
	}
}

func TestPublishRejectsOnlyRevokedNodes(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeRevoked); err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateSite(domain.Site{Name: "site", Domains: []string{"cdn.example.test"}, Nodes: []string{node.ID}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}}, "zone")
	if err == nil {
		t.Fatal("expected revoked-node validation error")
	}
}

func TestSiteDomainIsExclusive(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	first := domain.Site{Name: "first", Domains: []string{"cdn.example.test"}, Nodes: []string{node.ID}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}}
	if _, err := database.CreateSite(first, "zone"); err != nil {
		t.Fatal(err)
	}
	second := domain.Site{Name: "second", Domains: []string{"cdn.example.test"}, Nodes: []string{node.ID}, PrimaryOrigin: domain.Origin{URL: "https://other-origin.example.test", Enabled: true}}
	if _, err := database.CreateSite(second, "zone"); err == nil {
		t.Fatal("expected duplicate-domain error")
	}
}

func TestInternalCARenewsOnlyTheMatchingNodeCertificate(t *testing.T) {
	ca, err := LoadOrCreateInternalCA(filepath.Join(t.TempDir(), "pki"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "cdn-edge"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	issued, err := ca.SignCSR(csr, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	issuedBlock, _ := pem.Decode(issued)
	if issuedBlock == nil {
		t.Fatal("issued certificate is invalid")
	}
	if _, err := ca.SignRenewal(issuedBlock.Bytes, csr, "node-1"); err != nil {
		t.Fatalf("expected renewal to succeed: %v", err)
	}
	if _, err := ca.SignRenewal(issuedBlock.Bytes, csr, "node-2"); err == nil {
		t.Fatal("expected renewal with another node ID to fail")
	}
}

func TestStoreCertificateRejectsMismatchedPrivateKey(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, err := NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, _, notAfter := testCertificate(t, "cdn.example.test")
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherKeyDER, err := x509.MarshalPKCS8PrivateKey(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	publisher := Publisher{Store: database, Cipher: cipher}
	if err := publisher.StoreCertificate("site", certificate, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: otherKeyDER}), notAfter); err == nil {
		t.Fatal("expected mismatched private key to be rejected")
	}
}

func decodeCSRF(t *testing.T, body []byte) string {
	t.Helper()
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result["csrf_token"]
}

func decodeRecoveryCode(t *testing.T, body []byte) string {
	t.Helper()
	var result struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.RecoveryCodes) == 0 {
		t.Fatal("setup response did not include recovery codes")
	}
	return result.RecoveryCodes[0]
}
