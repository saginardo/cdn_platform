package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
)

func (s *Server) getSettings(response http.ResponseWriter, _ *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	writeJSON(response, http.StatusOK, s.Settings.View())
}

func (s *Server) getPublicBranding(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Cache-Control", "no-cache")
	branding := domain.DefaultBrandingSettings()
	if s.Settings != nil {
		branding = s.Settings.Branding()
	}
	writeJSON(response, http.StatusOK, branding)
}

type brandingSettingsRequest struct {
	Name        string  `json:"name"`
	Subtitle    string  `json:"subtitle"`
	LogoDataURL *string `json:"logo_data_url"`
}

func (s *Server) updateBrandingSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	var input brandingSettingsRequest
	if !readJSON(response, request, &input) {
		return
	}
	branding := s.Settings.Branding()
	branding.Name = input.Name
	branding.Subtitle = input.Subtitle
	if input.LogoDataURL != nil {
		branding.LogoDataURL = *input.LogoDataURL
	}
	if err := s.Settings.SaveBranding(branding); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	view := s.Settings.View().Branding
	s.audit(request, adminID(request.Context()), "update_branding", "settings", "branding", fmt.Sprintf("name=%q; subtitle_length=%d; logo_configured=%t", view.Name, len([]rune(view.Subtitle)), view.LogoDataURL != ""))
	writeJSON(response, http.StatusOK, view)
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

func (s *Server) updateCacheSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("settings are not configured"))
		return
	}
	var input struct {
		DefaultSizeGB int `json:"default_size_gb"`
	}
	if !readJSON(response, request, &input) {
		return
	}
	if err := s.Settings.SaveCacheDefaultSizeGB(input.DefaultSizeGB); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	s.audit(request, adminID(request.Context()), "update_cache", "settings", "cache", fmt.Sprintf("default_size_gb=%d", input.DefaultSizeGB))
	writeJSON(response, http.StatusOK, s.Settings.View().Cache)
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
	Enabled                bool      `json:"enabled"`
	Host                   string    `json:"host"`
	Port                   int       `json:"port"`
	Username               string    `json:"username"`
	Password               *string   `json:"password"`
	FromAddress            string    `json:"from_address"`
	Recipients             []string  `json:"recipients"`
	NotificationCategories *[]string `json:"notification_categories"`
	Security               string    `json:"security"`
}

func (input smtpSettingsRequest) profile(fallbackCategories []string) SMTPProfile {
	categories := fallbackCategories
	if input.NotificationCategories != nil {
		categories = *input.NotificationCategories
	}
	return SMTPProfile{Enabled: input.Enabled, Host: input.Host, Port: input.Port, Username: input.Username, FromAddress: input.FromAddress, Recipients: input.Recipients, NotificationCategories: categories, Security: input.Security}
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
	current, _ := s.Settings.SMTPProfile()
	if err := s.Settings.SaveSMTP(input.profile(current.NotificationCategories), input.Password); err != nil {
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
	current, currentPassword := s.Settings.SMTPProfile()
	profile := input.profile(current.NotificationCategories)
	if !profile.Enabled {
		writeError(response, http.StatusBadRequest, errors.New("enable SMTP before sending a test message"))
		return
	}
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
	notifier := integrations.Notifier(smtpNotifier(profile, password))
	if s.smtpNotifierFactory != nil {
		notifier = s.smtpNotifierFactory(profile, password)
	}
	if err := integrations.SendNotification(testCtx, notifier, integrations.Notification{
		Category: integrations.NotificationCategoryAvailability,
		Severity: integrations.NotificationSeverityInfo,
		Subject:  "CDN Platform SMTP 测试邮件",
		Message:  "SMTP 通知通道配置有效，控制面可以向当前收件人发送告警。",
		Details: []integrations.NotificationDetail{
			{Label: "SMTP 服务器", Value: net.JoinHostPort(profile.Host, fmt.Sprintf("%d", profile.Port))},
			{Label: "安全连接", Value: strings.ToUpper(profile.Security)},
			{Label: "收件人数", Value: fmt.Sprintf("%d", len(profile.Recipients))},
		},
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		status, responseError := smtpTestResponseError(err)
		if s.Logger != nil {
			s.Logger.Error("SMTP test failed", "host", profile.Host, "port", profile.Port, "security", profile.Security, "status", status, "error", err)
		}
		writeError(response, status, responseError)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func smtpTestResponseError(err error) (int, error) {
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return http.StatusGatewayTimeout, errors.New("SMTP connection timed out")
	}
	return http.StatusBadGateway, fmt.Errorf("SMTP test failed: %w", err)
}

type backupSettingsRequest struct {
	Repository         string  `json:"repository"`
	AccessKeyID        string  `json:"access_key_id"`
	SecretAccessKey    *string `json:"secret_access_key"`
	Region             string  `json:"region"`
	ResticPassword     *string `json:"restic_password"`
	BackupTime         string  `json:"backup_time"`
	RandomDelaySeconds int     `json:"random_delay_seconds"`
}

func (input backupSettingsRequest) settings() domain.BackupSettings {
	return domain.BackupSettings{
		Repository:         input.Repository,
		AccessKeyID:        input.AccessKeyID,
		Region:             input.Region,
		BackupTime:         input.BackupTime,
		RandomDelaySeconds: input.RandomDelaySeconds,
	}
}

func (s *Server) updateBackupSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("backup settings are not configured"))
		return
	}
	var input backupSettingsRequest
	if !readJSON(response, request, &input) {
		return
	}
	if err := s.Settings.SaveBackup(input.settings(), input.SecretAccessKey, input.ResticPassword); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	view := s.Settings.View().Backup
	s.audit(request, adminID(request.Context()), "update_backup", "settings", "backup",
		fmt.Sprintf("source=database; repository=%s; region=%s; backup_time=%s; random_delay_seconds=%d",
			view.Repository, view.Region, view.BackupTime, view.RandomDelaySeconds))
	writeJSON(response, http.StatusOK, view)
}

func (s *Server) clearBackupSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("backup settings are not configured"))
		return
	}
	if err := s.Settings.ClearBackup(); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	s.audit(request, adminID(request.Context()), "clear_backup", "settings", "backup", "restored environment fallback")
	writeJSON(response, http.StatusOK, s.Settings.View().Backup)
}

func (s *Server) testBackupSettings(response http.ResponseWriter, request *http.Request) {
	if s.Settings == nil || s.BackupValidator == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("backup repository validation is not configured"))
		return
	}
	var input backupSettingsRequest
	if !readJSON(response, request, &input) {
		return
	}
	runtime, err := s.Settings.ResolveBackup(input.settings(), input.SecretAccessKey, input.ResticPassword)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	validationCtx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	if err := s.BackupValidator.Validate(validationCtx, runtime); err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}
