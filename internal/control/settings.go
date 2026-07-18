package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/store"
)

const (
	SettingsSourceDatabase     = "database"
	SettingsSourceEnvironment  = "environment"
	SettingsSourceUnconfigured = "unconfigured"
)

type SMTPProfile struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username"`
	FromAddress string   `json:"from_address"`
	Recipients  []string `json:"recipients"`
	Security    string   `json:"security"`
}

type EnvironmentSettings struct {
	CloudflareAPIToken string
	SMTP               SMTPProfile
	SMTPPassword       string
	Backup             domain.BackupSettings
	BackupAccessKey    string
	BackupPassword     string
}

type CloudflareSettingsView struct {
	Source             string `json:"source"`
	Configured         bool   `json:"configured"`
	OverrideConfigured bool   `json:"override_configured"`
	EnvironmentSet     bool   `json:"environment_configured"`
}

type SMTPSettingsView struct {
	SMTPProfile
	Source             string `json:"source"`
	OverrideConfigured bool   `json:"override_configured"`
	PasswordConfigured bool   `json:"password_configured"`
	EnvironmentSet     bool   `json:"environment_configured"`
}

type BackupSettingsView struct {
	domain.BackupSettings
	Source                   string `json:"source"`
	Configured               bool   `json:"configured"`
	OverrideConfigured       bool   `json:"override_configured"`
	AccessKeyConfigured      bool   `json:"secret_access_key_configured"`
	ResticPasswordConfigured bool   `json:"restic_password_configured"`
	EnvironmentSet           bool   `json:"environment_configured"`
}

type SettingsView struct {
	Branding domain.BrandingSettings `json:"branding"`
	DNS      struct {
		DefaultTTLSeconds int `json:"default_ttl_seconds"`
	} `json:"dns"`
	Cloudflare CloudflareSettingsView `json:"cloudflare"`
	SMTP       SMTPSettingsView       `json:"smtp"`
	Backup     BackupSettingsView     `json:"backup"`
}

type SettingsManager struct {
	Store  *store.Store
	Cipher *Cipher

	updateMu     sync.Mutex
	mu           sync.RWMutex
	env          EnvironmentSettings
	dnsTTL       int
	branding     domain.BrandingSettings
	token        string
	tokenDB      bool
	smtp         SMTPProfile
	smtpPass     string
	smtpDB       bool
	backup       domain.BackupSettings
	backupSecret string
	backupPass   string
	backupDB     bool
}

func NewSettingsManager(database *store.Store, cipher *Cipher, environment EnvironmentSettings) (*SettingsManager, error) {
	if database == nil || cipher == nil {
		return nil, errors.New("settings store and cipher are required")
	}
	environment.CloudflareAPIToken = strings.TrimSpace(environment.CloudflareAPIToken)
	environment.SMTP = normalizeSMTPProfile(environment.SMTP)
	environment.Backup = domain.NormalizeBackupSettings(environment.Backup)
	manager := &SettingsManager{Store: database, Cipher: cipher, env: environment, dnsTTL: domain.DefaultDNSTTLSeconds}
	persisted, err := database.ControlSettings()
	if err != nil {
		return nil, err
	}
	manager.dnsTTL = persisted.DNSDefaultTTLSeconds
	manager.branding = domain.NormalizeBrandingSettings(persisted.Branding)
	manager.token = environment.CloudflareAPIToken
	if ciphertext, err := database.Secret(store.SecretCloudflareAPIToken); err == nil {
		plaintext, err := cipher.Decrypt(ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt Cloudflare API token: %w", err)
		}
		manager.token = string(plaintext)
		manager.tokenDB = true
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	manager.smtp = environment.SMTP
	manager.smtpPass = environment.SMTPPassword
	if persisted.SMTP.Override {
		manager.smtp = smtpProfileFromStore(persisted.SMTP)
		manager.smtpDB = true
		if ciphertext, err := database.Secret(store.SecretSMTPPassword); err == nil {
			plaintext, err := cipher.Decrypt(ciphertext)
			if err != nil {
				return nil, fmt.Errorf("decrypt SMTP password: %w", err)
			}
			manager.smtpPass = string(plaintext)
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		} else {
			manager.smtpPass = ""
		}
	}
	manager.backup = environment.Backup
	manager.backupSecret = environment.BackupAccessKey
	manager.backupPass = environment.BackupPassword
	if persisted.BackupOverride {
		manager.backup = domain.NormalizeBackupSettings(persisted.Backup)
		manager.backupDB = true
		ciphertext, err := database.Secret(store.SecretBackupAccessKey)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, errors.New("stored backup override is missing the S3 secret access key")
			}
			return nil, err
		}
		plaintext, err := cipher.Decrypt(ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt S3 secret access key: %w", err)
		}
		manager.backupSecret = string(plaintext)
		ciphertext, err = database.Secret(store.SecretBackupPassword)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, errors.New("stored backup override is missing the Restic repository password")
			}
			return nil, err
		}
		plaintext, err = cipher.Decrypt(ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt Restic repository password: %w", err)
		}
		manager.backupPass = string(plaintext)
	}
	return manager, nil
}

func (m *SettingsManager) View() SettingsView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var view SettingsView
	view.Branding = m.branding
	view.DNS.DefaultTTLSeconds = m.dnsTTL
	view.Cloudflare.OverrideConfigured = m.tokenDB
	view.Cloudflare.EnvironmentSet = m.env.CloudflareAPIToken != ""
	view.Cloudflare.Configured = strings.TrimSpace(m.token) != ""
	switch {
	case m.tokenDB:
		view.Cloudflare.Source = SettingsSourceDatabase
	case m.env.CloudflareAPIToken != "":
		view.Cloudflare.Source = SettingsSourceEnvironment
	default:
		view.Cloudflare.Source = SettingsSourceUnconfigured
	}
	view.SMTP.SMTPProfile = cloneSMTPProfile(m.smtp)
	view.SMTP.OverrideConfigured = m.smtpDB
	view.SMTP.PasswordConfigured = m.smtpPass != ""
	view.SMTP.EnvironmentSet = m.env.SMTP.Enabled
	switch {
	case m.smtpDB:
		view.SMTP.Source = SettingsSourceDatabase
	case m.env.SMTP.Enabled:
		view.SMTP.Source = SettingsSourceEnvironment
	default:
		view.SMTP.Source = SettingsSourceUnconfigured
	}
	view.Backup.BackupSettings = m.backup
	view.Backup.OverrideConfigured = m.backupDB
	view.Backup.AccessKeyConfigured = m.backupSecret != ""
	view.Backup.ResticPasswordConfigured = m.backupPass != ""
	view.Backup.EnvironmentSet = domain.ValidateBackupSettings(m.env.Backup, m.env.BackupAccessKey, m.env.BackupPassword) == nil
	view.Backup.Configured = domain.ValidateBackupSettings(m.backup, m.backupSecret, m.backupPass) == nil
	switch {
	case m.backupDB:
		view.Backup.Source = SettingsSourceDatabase
	case view.Backup.EnvironmentSet:
		view.Backup.Source = SettingsSourceEnvironment
	default:
		view.Backup.Source = SettingsSourceUnconfigured
	}
	return view
}

func (m *SettingsManager) SaveBranding(settings domain.BrandingSettings) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	settings = domain.NormalizeBrandingSettings(settings)
	if err := domain.ValidateBrandingSettings(settings); err != nil {
		return err
	}
	if err := m.Store.SaveBrandingSettings(settings); err != nil {
		return err
	}
	m.mu.Lock()
	m.branding = settings
	m.mu.Unlock()
	return nil
}

func (m *SettingsManager) DNSDefaultTTL() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dnsTTL
}

func (m *SettingsManager) DNSTTL(site domain.Site) int {
	if site.DNSTTLSeconds != nil {
		return *site.DNSTTLSeconds
	}
	return m.DNSDefaultTTL()
}

func (m *SettingsManager) CloudflareToken() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if strings.TrimSpace(m.token) == "" {
		return "", errors.New("CLOUDFLARE_API_TOKEN is not configured")
	}
	return m.token, nil
}

func (m *SettingsManager) SaveDNSDefaultTTL(seconds int) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := domain.ValidateDNSTTLSeconds(seconds); err != nil {
		return err
	}
	if err := m.Store.SaveDNSDefaultTTL(seconds); err != nil {
		return err
	}
	m.mu.Lock()
	m.dnsTTL = seconds
	m.mu.Unlock()
	return nil
}

func (m *SettingsManager) SaveCloudflareToken(token string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("Cloudflare API token is required")
	}
	ciphertext, err := m.Cipher.Encrypt([]byte(token))
	if err != nil {
		return err
	}
	if err := m.Store.SetSecret(store.SecretCloudflareAPIToken, ciphertext); err != nil {
		return err
	}
	m.mu.Lock()
	m.token = token
	m.tokenDB = true
	m.mu.Unlock()
	return nil
}

func (m *SettingsManager) ClearCloudflareToken() error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.Store.DeleteSecret(store.SecretCloudflareAPIToken); err != nil {
		return err
	}
	m.mu.Lock()
	m.token = m.env.CloudflareAPIToken
	m.tokenDB = false
	m.mu.Unlock()
	return nil
}

func (m *SettingsManager) SMTPProfile() (SMTPProfile, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSMTPProfile(m.smtp), m.smtpPass
}

func (m *SettingsManager) ValidateSMTP(profile SMTPProfile, password string) (SMTPProfile, error) {
	profile = normalizeSMTPProfile(profile)
	if profile.Port < 1 || profile.Port > 65535 {
		return SMTPProfile{}, errors.New("SMTP port must be between 1 and 65535")
	}
	if profile.Security != integrations.SMTPSecurityStartTLS && profile.Security != integrations.SMTPSecurityTLS {
		return SMTPProfile{}, errors.New("SMTP security must be starttls or tls")
	}
	for _, value := range append([]string{profile.Host, profile.Username, profile.FromAddress}, profile.Recipients...) {
		if strings.ContainsAny(value, "\r\n") {
			return SMTPProfile{}, errors.New("SMTP settings contain invalid characters")
		}
	}
	if !profile.Enabled {
		return profile, nil
	}
	if profile.Username != "" && password == "" {
		return SMTPProfile{}, errors.New("SMTP password is required when username is configured")
	}
	if err := smtpNotifier(profile, password).Validate(); err != nil {
		return SMTPProfile{}, err
	}
	return profile, nil
}

func (m *SettingsManager) SaveSMTP(profile SMTPProfile, password *string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.RLock()
	currentPassword := m.smtpPass
	wasOverride := m.smtpDB
	m.mu.RUnlock()
	resolvedPassword := currentPassword
	if password != nil {
		resolvedPassword = *password
	}
	profile = normalizeSMTPProfile(profile)
	if profile.Username == "" {
		resolvedPassword = ""
	}
	validated, err := m.ValidateSMTP(profile, resolvedPassword)
	if err != nil {
		return err
	}
	replacePassword := password != nil || !wasOverride || validated.Username == ""
	var encrypted []byte
	if replacePassword && resolvedPassword != "" {
		encrypted, err = m.Cipher.Encrypt([]byte(resolvedPassword))
		if err != nil {
			return err
		}
	}
	if err := m.Store.SaveSMTPSettings(smtpProfileToStore(validated), encrypted, replacePassword); err != nil {
		return err
	}
	m.mu.Lock()
	m.smtp = validated
	m.smtpPass = resolvedPassword
	m.smtpDB = true
	m.mu.Unlock()
	return nil
}

func (m *SettingsManager) ClearSMTP() error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.Store.ClearSMTPSettings(); err != nil {
		return err
	}
	m.mu.Lock()
	m.smtp = cloneSMTPProfile(m.env.SMTP)
	m.smtpPass = m.env.SMTPPassword
	m.smtpDB = false
	m.mu.Unlock()
	return nil
}

func (m *SettingsManager) Notify(ctx context.Context, subject, body string) error {
	profile, password := m.SMTPProfile()
	if !profile.Enabled {
		return nil
	}
	return smtpNotifier(profile, password).Notify(ctx, subject, body)
}

func smtpNotifier(profile SMTPProfile, password string) integrations.SMTPNotifier {
	return integrations.SMTPNotifier{Host: profile.Host, Port: profile.Port, Username: profile.Username, Password: password, From: profile.FromAddress, To: append([]string(nil), profile.Recipients...), Security: profile.Security}
}

func normalizeSMTPProfile(profile SMTPProfile) SMTPProfile {
	profile.Host = strings.TrimSpace(profile.Host)
	profile.Username = strings.TrimSpace(profile.Username)
	profile.FromAddress = strings.TrimSpace(profile.FromAddress)
	profile.Security = strings.ToLower(strings.TrimSpace(profile.Security))
	if profile.Port == 0 {
		if profile.Security == integrations.SMTPSecurityTLS {
			profile.Port = 465
		} else {
			profile.Port = 587
		}
	}
	seen := make(map[string]struct{}, len(profile.Recipients))
	recipients := make([]string, 0, len(profile.Recipients))
	for _, recipient := range profile.Recipients {
		recipient = strings.TrimSpace(recipient)
		if recipient == "" {
			continue
		}
		key := strings.ToLower(recipient)
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		recipients = append(recipients, recipient)
	}
	profile.Recipients = recipients
	return profile
}

func cloneSMTPProfile(profile SMTPProfile) SMTPProfile {
	profile.Recipients = append([]string{}, profile.Recipients...)
	return profile
}

func smtpProfileFromStore(settings store.SMTPSettings) SMTPProfile {
	return normalizeSMTPProfile(SMTPProfile{Enabled: settings.Enabled, Host: settings.Host, Port: settings.Port, Username: settings.Username, FromAddress: settings.FromAddress, Recipients: settings.Recipients, Security: settings.Security})
}

func smtpProfileToStore(profile SMTPProfile) store.SMTPSettings {
	return store.SMTPSettings{Override: true, Enabled: profile.Enabled, Host: profile.Host, Port: profile.Port, Username: profile.Username, FromAddress: profile.FromAddress, Recipients: append([]string{}, profile.Recipients...), Security: profile.Security}
}
