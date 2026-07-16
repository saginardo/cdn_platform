package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) getSettings(response http.ResponseWriter, _ *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	writeJSON(response, http.StatusOK, s.Settings.View())
}

func (s *Server) updateDNSSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	var input struct {
		DefaultTTLSeconds int `json:"default_ttl_seconds"`
	}
	if !readJSON(response, request, &input) {
		return
	}
	if err := s.Settings.SaveDNSDefaultTTL(input.DefaultTTLSeconds); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	s.audit(request, adminID(request.Context()), "update_dns", "settings", "dns", fmt.Sprintf("default_ttl_seconds=%d", input.DefaultTTLSeconds))
	writeJSON(response, http.StatusOK, s.Settings.View().DNS)
}

type cloudflareSettingsRequest struct {
	Token string `json:"token"`
}

func (s *Server) updateCloudflareSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil || s.Cloudflare == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("Cloudflare settings are not configured"))
		return
	}
	var input cloudflareSettingsRequest
	if !readJSON(response, request, &input) {
		return
	}
	token := strings.TrimSpace(input.Token)
	if token == "" {
		writeError(response, http.StatusBadRequest, errors.New("Cloudflare API token is required"))
		return
	}
	if err := s.validateCloudflareToken(request.Context(), token); err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	if err := s.Settings.SaveCloudflareToken(token); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	s.audit(request, adminID(request.Context()), "update_cloudflare", "settings", "cloudflare", "source=database; token validated")
	writeJSON(response, http.StatusOK, s.Settings.View().Cloudflare)
}

func (s *Server) clearCloudflareSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	if err := s.Settings.ClearCloudflareToken(); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	s.audit(request, adminID(request.Context()), "clear_cloudflare", "settings", "cloudflare", "restored environment fallback")
	writeJSON(response, http.StatusOK, s.Settings.View().Cloudflare)
}

func (s *Server) testCloudflareSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil || s.Cloudflare == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("Cloudflare settings are not configured"))
		return
	}
	var input cloudflareSettingsRequest
	if !readJSON(response, request, &input) {
		return
	}
	token := strings.TrimSpace(input.Token)
	if token == "" {
		var err error
		token, err = s.Settings.CloudflareToken()
		if err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
	}
	if err := s.validateCloudflareToken(request.Context(), token); err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) validateCloudflareToken(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("Cloudflare API token is required")
	}
	zones := make([]string, 0)
	sites, err := s.Store.ListSites()
	if err != nil {
		return err
	}
	for _, site := range sites {
		zones = append(zones, site.ZoneID)
	}
	publications, err := s.Store.ListSitePublications()
	if err != nil {
		return err
	}
	for _, publication := range publications {
		zones = append(zones, publication.Site.ZoneID)
	}
	validationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.Cloudflare.ValidateToken(validationCtx, token, zones)
}

type smtpSettingsRequest struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username"`
	Password    *string  `json:"password"`
	FromAddress string   `json:"from_address"`
	Recipients  []string `json:"recipients"`
	Security    string   `json:"security"`
}

func (input smtpSettingsRequest) profile() SMTPProfile {
	return SMTPProfile{Enabled: input.Enabled, Host: input.Host, Port: input.Port, Username: input.Username, FromAddress: input.FromAddress, Recipients: input.Recipients, Security: input.Security}
}

func (s *Server) updateSMTPSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	var input smtpSettingsRequest
	if !readJSON(response, request, &input) {
		return
	}
	if err := s.Settings.SaveSMTP(input.profile(), input.Password); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	view := s.Settings.View().SMTP
	s.audit(request, adminID(request.Context()), "update_smtp", "settings", "smtp", fmt.Sprintf("enabled=%t; host=%s; port=%d; security=%s; recipients=%d", view.Enabled, view.Host, view.Port, view.Security, len(view.Recipients)))
	writeJSON(response, http.StatusOK, view)
}

func (s *Server) clearSMTPSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	if err := s.Settings.ClearSMTP(); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	s.audit(request, adminID(request.Context()), "clear_smtp", "settings", "smtp", "restored environment fallback")
	writeJSON(response, http.StatusOK, s.Settings.View().SMTP)
}

func (s *Server) testSMTPSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	var input smtpSettingsRequest
	if !readJSON(response, request, &input) {
		return
	}
	profile := input.profile()
	if !profile.Enabled {
		writeError(response, http.StatusBadRequest, errors.New("enable SMTP before sending a test message"))
		return
	}
	_, currentPassword := s.Settings.SMTPProfile()
	password := currentPassword
	if input.Password != nil {
		password = *input.Password
	}
	if profile.Username == "" {
		password = ""
	}
	profile, err := s.Settings.ValidateSMTP(profile, password)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	testCtx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	if err := smtpNotifier(profile, password).Notify(testCtx, "CDN Platform SMTP test", "This message confirms that the CDN Platform SMTP settings can send notifications."); err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}
