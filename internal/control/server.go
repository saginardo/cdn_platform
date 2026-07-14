package control

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cdn-platform/internal/auth"
	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/logstore"
	"cdn-platform/internal/store"
)

//go:embed web/*
var embeddedWeb embed.FS

//go:embed uninstall-edge.sh
var uninstallEdgeScript string

type Server struct {
	Store              *store.Store
	Cipher             *Cipher
	CA                 *InternalCA
	Publisher          Publisher
	DNS                integrations.DNSProvider
	Issuer             integrations.CertificateIssuer
	CertificateManager *CertificateManager
	Notifier           integrations.Notifier
	Logs               logstore.Store
	ControlURL         string
	EdgeControlURL     string
	EdgeBinaryURL      string
	EdgeBinarySHA256   string
	EdgeBinaryPath     string
	SetupAllowCIDRs    []*net.IPNet
	TrustedProxyCIDRs  []*net.IPNet
	Logger             *slog.Logger
	loginMu            sync.Mutex
	loginHits          map[string][]time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /install-edge.sh", s.bootstrapEdgeScript)
	mux.HandleFunc("GET /install-edge.service", s.bootstrapEdgeService)
	mux.HandleFunc("GET /uninstall-edge.sh", s.uninstallEdgeScript)
	mux.HandleFunc("GET /downloads/cdn-edge-agent-linux-amd64", s.edgeBinary)
	mux.HandleFunc("GET /api/setup/status", s.setupStatus)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.requireAdmin(s.logout))
	mux.HandleFunc("GET /api/session", s.requireAdmin(s.session))
	mux.HandleFunc("GET /api/nodes", s.requireAdmin(s.listNodes))
	mux.HandleFunc("POST /api/nodes", s.requireAdmin(s.createNode))
	mux.HandleFunc("POST /api/nodes/{id}/enrollment-token", s.requireAdmin(s.createEnrollmentToken))
	mux.HandleFunc("POST /api/nodes/{id}/status", s.requireAdmin(s.setNodeStatus))
	mux.HandleFunc("POST /api/nodes/{id}/uninstall", s.requireAdmin(s.prepareNodeUninstall))
	mux.HandleFunc("GET /api/nodes/{id}/uninstall", s.requireAdmin(s.nodeUninstallStatus))
	mux.HandleFunc("POST /api/nodes/{id}/uninstall/command", s.requireAdmin(s.createNodeUninstallCommand))
	mux.HandleFunc("DELETE /api/nodes/{id}/uninstall", s.requireAdmin(s.cancelNodeUninstall))
	mux.HandleFunc("POST /api/nodes/{id}/uninstall/force-complete", s.requireAdmin(s.forceCompleteNodeUninstall))
	mux.HandleFunc("DELETE /api/nodes/{id}", s.requireAdmin(s.deleteNode))
	mux.HandleFunc("GET /api/sites", s.requireAdmin(s.listSites))
	mux.HandleFunc("POST /api/sites", s.requireAdmin(s.createSite))
	mux.HandleFunc("PUT /api/sites/{id}", s.requireAdmin(s.updateSite))
	mux.HandleFunc("POST /api/sites/{id}/publish", s.requireAdmin(s.publishSite))
	mux.HandleFunc("GET /api/sites/{id}/publish-status", s.requireAdmin(s.publishStatus))
	mux.HandleFunc("POST /api/sites/{id}/invalidate-cache", s.requireAdmin(s.invalidateCache))
	mux.HandleFunc("POST /api/sites/{id}/certificate", s.requireAdmin(s.issueCertificate))
	mux.HandleFunc("GET /api/sites/{id}/certificate-task", s.requireAdmin(s.latestCertificateTask))
	mux.HandleFunc("GET /api/sites/{id}/tls-status", s.requireAdmin(s.tlsStatus))
	mux.HandleFunc("GET /api/sites/{id}/origin-allowlist", s.requireAdmin(s.originAllowlist))
	mux.HandleFunc("GET /api/tasks/{id}", s.requireAdmin(s.getTask))
	mux.HandleFunc("GET /api/sites/{id}/logs", s.requireAdmin(s.siteLogs))
	mux.HandleFunc("GET /api/sites/{id}/metrics", s.requireAdmin(s.siteMetrics))
	mux.HandleFunc("POST /api/edge/v1/enroll", s.enroll)
	mux.HandleFunc("POST /api/edge/v1/renew", s.requireEdge(s.renew))
	mux.HandleFunc("GET /api/edge/v1/desired-state", s.requireEdge(s.desiredState))
	mux.HandleFunc("POST /api/edge/v1/heartbeat", s.requireEdge(s.heartbeat))
	mux.HandleFunc("POST /api/edge/v1/logs", s.requireEdge(s.writeLogs))
	mux.HandleFunc("POST /api/edge/v1/uninstall/start", s.startNodeUninstall)
	mux.HandleFunc("POST /api/edge/v1/uninstall/fail", s.failNodeUninstall)
	mux.HandleFunc("POST /api/edge/v1/uninstall/complete", s.completeNodeUninstall)
	web, err := fs.Sub(embeddedWeb, "web")
	if err == nil {
		mux.Handle("/", http.FileServer(http.FS(web)))
	}
	return s.withSecurityHeaders(s.withRequestLog(mux))
}

func (s *Server) TLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	if s.CA != nil {
		pool.AddCert(s.CA.Certificate)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool}
}

func ResolveEdgeBinarySHA256(path, configured string) (string, error) {
	configured = strings.ToLower(strings.TrimSpace(configured))
	if path == "" {
		if configured == "" {
			return "", nil
		}
		if !validSHA256Digest(configured) {
			return "", errors.New("EDGE_BINARY_SHA256 must be a 64-character hexadecimal digest")
		}
		return configured, nil
	}
	contents, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read EDGE_BINARY_PATH: %w", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(contents))
	if configured != "" && configured != digest {
		return "", fmt.Errorf("EDGE_BINARY_SHA256 does not match EDGE_BINARY_PATH: got %s, want %s", configured, digest)
	}
	return digest, nil
}

func (s *Server) health(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) setupStatus(response http.ResponseWriter, request *http.Request) {
	hasAdmin, err := s.Store.HasAdmin()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"initialized": hasAdmin})
}

type setupRequest struct {
	Password   string `json:"password"`
	TOTPSecret string `json:"totp_secret"`
}

func (s *Server) setup(response http.ResponseWriter, request *http.Request) {
	if len(s.SetupAllowCIDRs) > 0 && !s.setupIPAllowed(s.requestIP(request)) {
		writeError(response, http.StatusForbidden, errors.New("setup is not allowed from this address"))
		return
	}
	hasAdmin, err := s.Store.HasAdmin()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if hasAdmin {
		writeError(response, http.StatusConflict, errors.New("control plane is already initialized"))
		return
	}
	var input setupRequest
	if !readJSON(response, request, &input) {
		return
	}
	if input.TOTPSecret == "" {
		input.TOTPSecret, err = auth.NewTOTPSecret()
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
	} else {
		input.TOTPSecret = auth.NormalizeTOTPSecret(input.TOTPSecret)
		if !auth.ValidTOTPSecret(input.TOTPSecret) {
			writeError(response, http.StatusBadRequest, errors.New("invalid TOTP secret"))
			return
		}
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if err := s.Store.CreateInitialAdmin(passwordHash, input.TOTPSecret); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	recoveryCodes, err := newRecoveryCodes(10)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	hashes := make([]string, 0, len(recoveryCodes))
	for _, code := range recoveryCodes {
		hashes = append(hashes, auth.RecoveryCodeHash(code))
	}
	if err := s.Store.ReplaceRecoveryCodes("admin", hashes); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	s.audit(request, "bootstrap", "admin", "admin", "admin", "created initial admin")
	writeJSON(response, http.StatusCreated, map[string]any{"totp_secret": input.TOTPSecret, "otpauth_url": "otpauth://totp/CDN%20Platform:admin?secret=" + input.TOTPSecret + "&issuer=CDN%20Platform", "recovery_codes": recoveryCodes})
}

func (s *Server) setupIPAllowed(address string) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}
	for _, cidr := range s.SetupAllowCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

type loginRequest struct {
	Password     string `json:"password"`
	TOTP         string `json:"totp"`
	RecoveryCode string `json:"recovery_code"`
}

func (s *Server) login(response http.ResponseWriter, request *http.Request) {
	if !s.allowLogin(s.requestIP(request)) {
		writeError(response, http.StatusTooManyRequests, errors.New("too many login attempts"))
		return
	}
	admin, err := s.Store.Admin()
	if err != nil {
		writeError(response, http.StatusUnauthorized, errors.New("invalid credentials"))
		return
	}
	var input loginRequest
	if !readJSON(response, request, &input) {
		return
	}
	if !auth.VerifyPassword(admin.PasswordHash, input.Password) {
		writeError(response, http.StatusUnauthorized, errors.New("invalid credentials"))
		return
	}
	validSecondFactor := auth.VerifyTOTP(admin.TOTPSecret, input.TOTP, time.Now())
	if !validSecondFactor && input.RecoveryCode != "" {
		userID, recoveryErr := s.Store.ConsumeRecoveryCode(auth.RecoveryCodeHash(input.RecoveryCode))
		validSecondFactor = recoveryErr == nil && userID == admin.ID
	}
	if !validSecondFactor {
		writeError(response, http.StatusUnauthorized, errors.New("invalid second factor"))
		return
	}
	token, err := auth.NewOpaqueToken(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	csrf, err := auth.NewOpaqueToken(24)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if err := s.Store.CreateSession(admin.ID, token, csrf, time.Now().UTC().Add(12*time.Hour)); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(response, &http.Cookie{Name: "cdn_session", Value: token, Path: "/", HttpOnly: true, Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds())})
	s.audit(request, admin.ID, "login", "session", "", "")
	writeJSON(response, http.StatusOK, map[string]string{"csrf_token": csrf})
}

func (s *Server) logout(response http.ResponseWriter, request *http.Request) {
	cookie, _ := request.Cookie("cdn_session")
	if cookie != nil {
		_ = s.Store.DeleteSession(cookie.Value)
	}
	http.SetCookie(response, &http.Cookie{Name: "cdn_session", Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteStrictMode})
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) session(response http.ResponseWriter, request *http.Request) {
	cookie, err := request.Cookie("cdn_session")
	if err != nil {
		writeError(response, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	session, err := s.Store.Session(cookie.Value)
	if err != nil {
		writeError(response, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"user": adminID(request.Context()), "csrf_token": session.CSRFToken})
}

func (s *Server) listNodes(response http.ResponseWriter, request *http.Request) {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, nodes)
}

type nodeRequest struct {
	Name       string `json:"name"`
	PublicIPv4 string `json:"public_ipv4"`
}

func (s *Server) createNode(response http.ResponseWriter, request *http.Request) {
	var input nodeRequest
	if !readJSON(response, request, &input) {
		return
	}
	node, err := s.Store.CreateNode(input.Name, input.PublicIPv4)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	s.audit(request, adminID(request.Context()), "create", "node", node.ID, node.Name)
	writeJSON(response, http.StatusCreated, node)
}

func (s *Server) createEnrollmentToken(response http.ResponseWriter, request *http.Request) {
	nodeID := request.PathValue("id")
	digest := strings.TrimSpace(s.EdgeBinarySHA256)
	edgeControlURL := s.edgeControlURL()
	if !validHTTPSURL(s.ControlURL) || !validHTTPSURL(edgeControlURL) || !validHTTPSURL(s.EdgeBinaryURL) || !validSHA256Digest(digest) {
		writeError(response, http.StatusConflict, errors.New("CONTROL_PUBLIC_URL, EDGE_CONTROL_URL, and EDGE_BINARY_URL must be HTTPS URLs, and EDGE_BINARY_SHA256 must be a 64-character digest before generating an enrollment command"))
		return
	}
	enrollmentRequired, err := s.Store.NodeRequiresEnrollment(nodeID)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	bootstrapURL := strings.TrimRight(s.ControlURL, "/") + "/install-edge.sh"
	result := map[string]any{"enrollment_required": enrollmentRequired}
	if enrollmentRequired {
		token, err := auth.NewOpaqueToken(32)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		expiresAt := time.Now().UTC().Add(15 * time.Minute)
		if err := s.Store.CreateEnrollmentToken(nodeID, token, expiresAt); err != nil {
			writeStoreError(response, err)
			return
		}
		s.audit(request, adminID(request.Context()), "create_enrollment_token", "node", nodeID, "expires "+expiresAt.Format(time.RFC3339))
		result["token"] = token
		result["expires_at"] = expiresAt
		result["install_command"] = fmt.Sprintf("curl -fsSL %q | sudo bash -s -- --control-url %q --enrollment-token %q --binary-url %q --binary-sha256 %q", bootstrapURL, edgeControlURL, token, s.EdgeBinaryURL, digest)
	} else {
		s.audit(request, adminID(request.Context()), "create_upgrade_command", "node", nodeID, "preserve existing mTLS identity")
		result["install_command"] = fmt.Sprintf("curl -fsSL %q | sudo bash -s -- --control-url %q --binary-url %q --binary-sha256 %q", bootstrapURL, edgeControlURL, s.EdgeBinaryURL, digest)
	}
	writeJSON(response, http.StatusCreated, result)
}

func validSHA256Digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func (s *Server) bootstrapEdgeScript(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write([]byte(bootstrapEdgeScript))
}

func (s *Server) bootstrapEdgeService(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write([]byte(bootstrapEdgeService))
}

func (s *Server) edgeBinary(response http.ResponseWriter, request *http.Request) {
	path := strings.TrimSpace(s.EdgeBinaryPath)
	info, err := os.Stat(path)
	if path == "" || err != nil || !info.Mode().IsRegular() {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Content-Disposition", "attachment; filename=cdn-edge-agent-linux-amd64")
	response.Header().Set("Cache-Control", "no-store")
	http.ServeFile(response, request, path)
}

type statusRequest struct {
	Status domain.NodeStatus `json:"status"`
}

func (s *Server) setNodeStatus(response http.ResponseWriter, request *http.Request) {
	var input statusRequest
	if !readJSON(response, request, &input) {
		return
	}
	if input.Status != domain.NodeDraining && input.Status != domain.NodeRevoked && input.Status != domain.NodeActive {
		writeError(response, http.StatusBadRequest, errors.New("status must be active, draining, or revoked"))
		return
	}
	if err := s.Store.SetNodeStatus(request.PathValue("id"), input.Status); err != nil {
		writeStoreError(response, err)
		return
	}
	s.audit(request, adminID(request.Context()), "set_status", "node", request.PathValue("id"), string(input.Status))
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listSites(response http.ResponseWriter, request *http.Request) {
	sites, err := s.Store.ListSites()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, sites)
}

type siteRequest struct {
	Name                string         `json:"name"`
	ZoneID              string         `json:"zone_id"`
	Domains             []string       `json:"domains"`
	NodeIDs             []string       `json:"node_ids"`
	PrimaryOrigin       domain.Origin  `json:"primary_origin"`
	BackupOrigin        *domain.Origin `json:"backup_origin"`
	StreamPaths         *[]string      `json:"stream_paths"`
	Passthrough         *bool          `json:"passthrough"`
	ClientMaxBodySizeMB *int           `json:"client_max_body_size_mb"`
	Enabled             *bool          `json:"enabled"`
}

func (input siteRequest) site(id string) domain.Site {
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	var streamPaths []string
	if input.StreamPaths != nil {
		streamPaths = *input.StreamPaths
	}
	passthrough := false
	if input.Passthrough != nil {
		passthrough = *input.Passthrough
	}
	clientMaxBodySizeMB := domain.DefaultClientMaxBodySizeMB
	if input.ClientMaxBodySizeMB != nil {
		clientMaxBodySizeMB = *input.ClientMaxBodySizeMB
	}
	return domain.Site{ID: id, Name: input.Name, Domains: input.Domains, Nodes: input.NodeIDs, PrimaryOrigin: input.PrimaryOrigin, BackupOrigin: input.BackupOrigin, StreamPaths: streamPaths, Passthrough: passthrough, ClientMaxBodySizeMB: clientMaxBodySizeMB, Enabled: enabled}
}

func (input siteRequest) validateClientMaxBodySize() error {
	if input.ClientMaxBodySizeMB == nil {
		return nil
	}
	return domain.ValidateClientMaxBodySizeMB(*input.ClientMaxBodySizeMB)
}

func (s *Server) createSite(response http.ResponseWriter, request *http.Request) {
	var input siteRequest
	if !readJSON(response, request, &input) {
		return
	}
	if err := input.validateClientMaxBodySize(); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	site, err := s.Store.CreateSite(input.site(""), input.ZoneID)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	s.audit(request, adminID(request.Context()), "create", "site", site.ID, site.Name)
	writeJSON(response, http.StatusCreated, site)
}

func (s *Server) updateSite(response http.ResponseWriter, request *http.Request) {
	var input siteRequest
	if !readJSON(response, request, &input) {
		return
	}
	current, _, err := s.Store.GetSite(request.PathValue("id"))
	if err != nil {
		writeStoreError(response, err)
		return
	}
	if err := input.validateClientMaxBodySize(); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	siteInput := input.site(request.PathValue("id"))
	if input.Enabled == nil {
		siteInput.Enabled = current.Enabled
	}
	if input.StreamPaths == nil {
		siteInput.StreamPaths = current.StreamPaths
	}
	if input.Passthrough == nil {
		siteInput.Passthrough = current.Passthrough
	}
	if input.ClientMaxBodySizeMB == nil {
		siteInput.ClientMaxBodySizeMB = current.ClientMaxBodySizeMB
	}
	site, err := s.Store.UpdateSite(siteInput, input.ZoneID)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	s.audit(request, adminID(request.Context()), "update", "site", site.ID, site.Name)
	writeJSON(response, http.StatusOK, site)
}

func (s *Server) publishSite(response http.ResponseWriter, request *http.Request) {
	task, err := s.Publisher.PublishSite(request.PathValue("id"))
	if err != nil {
		writeStoreError(response, err)
		return
	}
	s.audit(request, adminID(request.Context()), "publish", "site", request.PathValue("id"), task.ID)
	writeJSON(response, http.StatusAccepted, task)
}

func (s *Server) publishStatus(response http.ResponseWriter, request *http.Request) {
	if _, _, err := s.Store.GetSite(request.PathValue("id")); err != nil {
		writeStoreError(response, err)
		return
	}
	status, err := s.Store.PublishStatus(request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, status)
}

func (s *Server) invalidateCache(response http.ResponseWriter, request *http.Request) {
	site, err := s.Store.InvalidateSiteCache(request.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrCacheDisabled) {
			writeError(response, http.StatusConflict, err)
			return
		}
		writeStoreError(response, err)
		return
	}
	task, err := s.Publisher.PublishSite(site.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	s.audit(request, adminID(request.Context()), "invalidate_cache", "site", site.ID, fmt.Sprintf("generation=%d task=%s", site.CacheGeneration, task.ID))
	writeJSON(response, http.StatusAccepted, task)
}

func (s *Server) issueCertificate(response http.ResponseWriter, request *http.Request) {
	if s.CertificateManager == nil {
		writeError(response, http.StatusNotImplemented, errors.New("certificate issuer is not configured"))
		return
	}
	site, _, err := s.Store.GetSite(request.PathValue("id"))
	if err != nil {
		writeStoreError(response, err)
		return
	}
	task, created, err := s.CertificateManager.QueueIssue(site)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, err)
		return
	}
	detail := task.ID
	if !created {
		detail += " reused"
	}
	s.audit(request, adminID(request.Context()), "issue_certificate", "site", site.ID, detail)
	writeJSON(response, http.StatusAccepted, task)
}

func (s *Server) latestCertificateTask(response http.ResponseWriter, request *http.Request) {
	if _, _, err := s.Store.GetSite(request.PathValue("id")); err != nil {
		writeStoreError(response, err)
		return
	}
	task, err := s.Store.LatestCertificateTask(request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(response, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, task)
}

type tlsStatusResponse struct {
	CertificateTask           *domain.DeploymentTask `json:"certificate_task"`
	PublishedAfterCertificate bool                   `json:"published_after_certificate"`
}

func (s *Server) tlsStatus(response http.ResponseWriter, request *http.Request) {
	site, _, err := s.Store.GetSite(request.PathValue("id"))
	if err != nil {
		writeStoreError(response, err)
		return
	}
	task, err := s.Store.LatestCertificateTask(site.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(response, http.StatusOK, tlsStatusResponse{})
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	status := tlsStatusResponse{CertificateTask: &task}
	if task.Status == domain.TaskSucceeded {
		published, err := s.Store.HasSuccessfulPublishAfter(site.ID, task.UpdatedAt)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		status.PublishedAfterCertificate = published
	}
	writeJSON(response, http.StatusOK, status)
}

func (s *Server) originAllowlist(response http.ResponseWriter, request *http.Request) {
	site, _, err := s.Store.GetSite(request.PathValue("id"))
	if err != nil {
		writeStoreError(response, err)
		return
	}
	addresses := make([]string, 0, len(site.Nodes))
	for _, nodeID := range site.Nodes {
		node, err := s.Store.GetNode(nodeID)
		if err != nil || node.Status == domain.NodeRevoked || node.Status == domain.NodeUninstalling || node.Status == domain.NodeUninstalled {
			continue
		}
		addresses = append(addresses, node.PublicIPv4+"/32")
	}
	writeJSON(response, http.StatusOK, map[string]any{"site_id": site.ID, "ipv4_cidrs": addresses, "note": "Allow only these edge IPv4 CIDRs at the origin firewall or security group. Update after adding, removing, or revoking a node."})
}

func (s *Server) getTask(response http.ResponseWriter, request *http.Request) {
	task, err := s.Store.GetTask(request.PathValue("id"))
	if err != nil {
		writeStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, task)
}

func (s *Server) siteLogs(response http.ResponseWriter, request *http.Request) {
	if s.Logs == nil {
		writeJSON(response, http.StatusOK, []domain.AccessLogEvent{})
		return
	}
	events, err := s.Logs.Recent(request.Context(), request.PathValue("id"), 100)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, events)
}

func (s *Server) siteMetrics(response http.ResponseWriter, request *http.Request) {
	if s.Logs == nil {
		writeJSON(response, http.StatusOK, []logstore.MinuteMetric{})
		return
	}
	metrics, err := s.Logs.Metrics(request.Context(), request.PathValue("id"), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, metrics)
}

type enrollRequest struct {
	EnrollmentToken string `json:"enrollment_token"`
	CSR             string `json:"csr"`
}

func (s *Server) enroll(response http.ResponseWriter, request *http.Request) {
	if s.CA == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("internal CA is not configured"))
		return
	}
	var input enrollRequest
	if !readJSON(response, request, &input) {
		return
	}
	if _, err := ParseAndVerifyCSR([]byte(input.CSR)); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	nodeID, err := s.Store.ConsumeEnrollmentToken(input.EnrollmentToken)
	if err != nil {
		writeError(response, http.StatusUnauthorized, store.ErrTokenInvalid)
		return
	}
	certificate, err := s.CA.SignCSR([]byte(input.CSR), nodeID)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	fingerprint, err := CertificateFingerprintPEM(certificate)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if err := s.Store.SetNodeCertificate(nodeID, fingerprint); err != nil {
		writeStoreError(response, err)
		return
	}
	s.audit(request, "edge:"+nodeID, "enroll", "node", nodeID, fingerprint)
	writeJSON(response, http.StatusCreated, map[string]string{"node_id": nodeID, "client_certificate": string(certificate), "ca_certificate": string(s.CA.CertificatePEM)})
}

func (s *Server) renew(response http.ResponseWriter, request *http.Request) {
	if s.CA == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("internal CA is not configured"))
		return
	}
	var input struct {
		CSR string `json:"csr"`
	}
	if !readJSON(response, request, &input) {
		return
	}
	if _, err := ParseAndVerifyCSR([]byte(input.CSR)); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	certificate, err := s.CA.SignRenewal(request.TLS.PeerCertificates[0].Raw, []byte(input.CSR), edgeNodeID(request.Context()))
	if err != nil {
		writeError(response, http.StatusUnauthorized, err)
		return
	}
	fingerprint, err := CertificateFingerprintPEM(certificate)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if err := s.Store.SetNodeCertificate(edgeNodeID(request.Context()), fingerprint); err != nil {
		writeStoreError(response, err)
		return
	}
	s.audit(request, "edge:"+edgeNodeID(request.Context()), "renew", "node", edgeNodeID(request.Context()), fingerprint)
	writeJSON(response, http.StatusOK, map[string]string{"client_certificate": string(certificate), "ca_certificate": string(s.CA.CertificatePEM)})
}

func (s *Server) desiredState(response http.ResponseWriter, request *http.Request) {
	nodeID := edgeNodeID(request.Context())
	state, encryptedCertificates, err := s.Store.NodeState(nodeID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(response, http.StatusOK, domain.DesiredState{Version: 0, NginxConfig: ""})
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if len(encryptedCertificates) > 0 {
		plaintext, err := s.Cipher.Decrypt(encryptedCertificates)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		if err := json.Unmarshal(plaintext, &state.Certificates); err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(response, http.StatusOK, state)
}

type heartbeatRequest struct {
	LastError      string              `json:"last_error"`
	AppliedVersion int64               `json:"applied_version"`
	ApplyReport    *domain.ApplyReport `json:"apply_report,omitempty"`
}

func (s *Server) heartbeat(response http.ResponseWriter, request *http.Request) {
	var input heartbeatRequest
	if !readJSON(response, request, &input) {
		return
	}
	nodeID := edgeNodeID(request.Context())
	if err := s.Store.Heartbeat(nodeID, input.AppliedVersion, input.LastError, input.ApplyReport); err != nil {
		writeStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) writeLogs(response http.ResponseWriter, request *http.Request) {
	if s.Logs == nil {
		writeJSON(response, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	var events []domain.AccessLogEvent
	decoder := json.NewDecoder(io.LimitReader(request.Body, 8<<20))
	if err := decoder.Decode(&events); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if len(events) > 500 {
		writeError(response, http.StatusRequestEntityTooLarge, errors.New("a log batch may contain at most 500 events"))
		return
	}
	nodeID := edgeNodeID(request.Context())
	sites, err := s.Store.ListSites()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	allowedSites := make(map[string]struct{})
	for _, site := range sites {
		for _, assignedNodeID := range site.Nodes {
			if assignedNodeID == nodeID {
				allowedSites[site.ID] = struct{}{}
				break
			}
		}
	}
	accepted := events[:0]
	for index := range events {
		if _, allowed := allowedSites[events[index].SiteID]; !allowed {
			continue
		}
		events[index].NodeID = nodeID
		events[index].Path = strings.SplitN(events[index].Path, "?", 2)[0]
		if events[index].Timestamp.IsZero() {
			events[index].Timestamp = time.Now().UTC()
		}
		accepted = append(accepted, events[index])
	}
	if err := s.Logs.Append(request.Context(), accepted); err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]int{"accepted": len(accepted)})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("cdn_session")
		if err != nil {
			writeError(response, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		session, err := s.Store.Session(cookie.Value)
		if err != nil {
			writeError(response, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions {
			if request.Header.Get("X-CSRF-Token") == "" || request.Header.Get("X-CSRF-Token") != session.CSRFToken {
				writeError(response, http.StatusForbidden, errors.New("invalid CSRF token"))
				return
			}
		}
		next(response, request.WithContext(context.WithValue(request.Context(), adminContextKey{}, session.UserID)))
	}
}

func (s *Server) requireEdge(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
			writeError(response, http.StatusUnauthorized, errors.New("mTLS client certificate required"))
			return
		}
		fingerprint := CertificateFingerprintDER(request.TLS.PeerCertificates[0].Raw)
		nodeID, err := s.Store.NodeIDByFingerprint(fingerprint)
		if err != nil {
			writeError(response, http.StatusUnauthorized, errors.New("edge certificate is not authorized"))
			return
		}
		next(response, request.WithContext(context.WithValue(request.Context(), edgeContextKey{}, nodeID)))
	}
}

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Referrer-Policy", "same-origin")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(response, request)
	})
}

func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(response, request)
		if s.Logger != nil {
			s.Logger.Info("request", "method", request.Method, "path", request.URL.Path, "remote", s.requestIP(request), "duration", time.Since(started).String())
		}
	})
}

func (s *Server) audit(request *http.Request, actor, action, resourceType, resourceID, detail string) {
	_ = s.Store.Audit(actor, action, resourceType, resourceID, s.requestIP(request), detail)
}

func (s *Server) allowLogin(remoteAddr string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.loginHits == nil {
		s.loginHits = make(map[string][]time.Time)
	}
	key := remoteIP(remoteAddr)
	now := time.Now()
	window := now.Add(-10 * time.Minute)
	hits := s.loginHits[key]
	filtered := hits[:0]
	for _, hit := range hits {
		if hit.After(window) {
			filtered = append(filtered, hit)
		}
	}
	if len(filtered) >= 8 {
		s.loginHits[key] = filtered
		return false
	}
	s.loginHits[key] = append(filtered, now)
	return true
}

type adminContextKey struct{}
type edgeContextKey struct{}

func adminID(context context.Context) string {
	value, _ := context.Value(adminContextKey{}).(string)
	return value
}
func edgeNodeID(context context.Context) string {
	value, _ := context.Value(edgeContextKey{}).(string)
	return value
}
func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

func (s *Server) edgeControlURL() string {
	if value := strings.TrimRight(strings.TrimSpace(s.EdgeControlURL), "/"); value != "" {
		return value
	}
	return strings.TrimRight(strings.TrimSpace(s.ControlURL), "/")
}

func (s *Server) requestIP(request *http.Request) string {
	peer := remoteIP(request.RemoteAddr)
	parsedPeer := net.ParseIP(peer)
	if parsedPeer == nil || !s.isTrustedProxy(parsedPeer) {
		return peer
	}
	if forwarded := net.ParseIP(strings.TrimSpace(request.Header.Get("X-Real-IP"))); forwarded != nil {
		return forwarded.String()
	}
	return peer
}

func (s *Server) isTrustedProxy(address net.IP) bool {
	for _, cidr := range s.TrustedProxyCIDRs {
		if cidr.Contains(address) {
			return true
		}
	}
	return false
}
func newRecoveryCodes(count int) ([]string, error) {
	codes := make([]string, 0, count)
	for range count {
		code, err := auth.NewRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

func readJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return false
	}
	return true
}
func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
func writeError(response http.ResponseWriter, status int, err error) {
	writeJSON(response, status, map[string]string{"error": err.Error()})
}
func writeStoreError(response http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, store.ErrUninstallActive) || errors.Is(err, store.ErrUninstallNotActive) || errors.Is(err, store.ErrNodeAssigned) {
		writeError(response, http.StatusConflict, err)
		return
	}
	writeError(response, http.StatusBadRequest, err)
}
