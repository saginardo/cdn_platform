package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cdn-platform/internal/control"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/logstore"
	"cdn-platform/internal/store"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "keygen" {
		key, err := control.NewEncryptionKey()
		if err != nil {
			fatal(err.Error())
		}
		fmt.Println(key)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "publish-all" {
		publishAll()
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "cloudflare-credentials" {
		writeCloudflareCredentials(os.Args[2])
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	dataDir := env("CONTROL_DATA_DIR", "/var/lib/cdn-platform")
	key := os.Getenv("CONTROL_ENCRYPTION_KEY")
	if key == "" {
		fatal("CONTROL_ENCRYPTION_KEY is required; generate it with: cdn-control keygen")
	}
	cipher, err := control.NewCipher(key)
	if err != nil {
		fatal(err.Error())
	}
	database, err := store.Open(filepath.Join(dataDir, "control.db"))
	if err != nil {
		fatal("open database: " + err.Error())
	}
	defer database.Close()
	ca, err := control.LoadOrCreateInternalCA(filepath.Join(dataDir, "pki"))
	if err != nil {
		fatal("load internal CA: " + err.Error())
	}
	environment, err := settingsFromEnv()
	if err != nil {
		fatal(err.Error())
	}
	settings, err := control.NewSettingsManager(database, cipher, environment)
	if err != nil {
		fatal("load settings: " + err.Error())
	}
	dns := &integrations.CloudflareDNS{Token: settings.CloudflareToken}
	issuer := integrations.CertbotDNSIssuer{Email: os.Getenv("ACME_EMAIL"), ConfigDir: filepath.Join(dataDir, "letsencrypt"), Token: settings.CloudflareToken}
	var logs logstore.Store = logstore.Noop{}
	if os.Getenv("CLICKHOUSE_DISABLED") != "1" {
		clickhouse := logstore.ClickHouse{Endpoint: env("CLICKHOUSE_URL", "http://127.0.0.1:8123"), Database: env("CLICKHOUSE_DATABASE", "cdn_platform"), Username: os.Getenv("CLICKHOUSE_USER"), Password: os.Getenv("CLICKHOUSE_PASSWORD")}
		if err := clickhouse.EnsureSchema(context.Background()); err != nil {
			logger.Warn("ClickHouse schema setup failed; log writes will retry", "error", err)
		}
		logs = clickhouse
	}
	publisher := control.Publisher{Store: database, Cipher: cipher}
	var notifier integrations.Notifier = settings
	issueTimeout := 10 * time.Minute
	if value := strings.TrimSpace(os.Getenv("CERTIFICATE_ISSUE_TIMEOUT")); value != "" {
		issueTimeout, err = time.ParseDuration(value)
		if err != nil || issueTimeout <= 0 {
			fatal("CERTIFICATE_ISSUE_TIMEOUT must be a positive Go duration")
		}
	}
	certificateManager := &control.CertificateManager{Store: database, Publisher: publisher, Issuer: issuer, Notifier: notifier, IssueTimeout: issueTimeout}
	siteDeleter := &control.SiteDeletionManager{Store: database, Publisher: publisher, DNS: dns, Certificates: issuer}
	setupCIDRs, err := parseCIDRs(os.Getenv("SETUP_ALLOW_CIDRS"))
	if err != nil {
		fatal("SETUP_ALLOW_CIDRS: " + err.Error())
	}
	trustedProxyCIDRs, err := parseCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		fatal("TRUSTED_PROXY_CIDRS: " + err.Error())
	}
	controlURL := env("CONTROL_PUBLIC_URL", "https://control.example.invalid")
	edgeBinaryPath := os.Getenv("EDGE_BINARY_PATH")
	edgeBinarySHA256, err := control.ResolveEdgeBinarySHA256(edgeBinaryPath, os.Getenv("EDGE_BINARY_SHA256"))
	if err != nil {
		fatal(err.Error())
	}
	server := &control.Server{Store: database, Cipher: cipher, CA: ca, Publisher: publisher, DNS: dns, Cloudflare: dns, Issuer: issuer, CertificateManager: certificateManager, SiteDeleter: siteDeleter, Settings: settings, Notifier: notifier, Logs: logs, ControlURL: controlURL, EdgeControlURL: env("EDGE_CONTROL_URL", controlURL), EdgeBinaryURL: os.Getenv("EDGE_BINARY_URL"), EdgeBinarySHA256: edgeBinarySHA256, EdgeBinaryPath: edgeBinaryPath, SetupAllowCIDRs: setupCIDRs, TrustedProxyCIDRs: trustedProxyCIDRs, Logger: logger}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	healthManager := &control.HealthManager{Server: server}
	certificateManager.Start(ctx)
	defer certificateManager.Stop()
	go healthManager.Run(ctx)
	go certificateManager.Run(ctx)
	certificatePath, privateKeyPath := os.Getenv("CONTROL_TLS_CERT"), os.Getenv("CONTROL_TLS_KEY")
	if certificatePath == "" || privateKeyPath == "" {
		fatal("CONTROL_TLS_CERT and CONTROL_TLS_KEY are required")
	}
	certificate, err := control.NewReloadingCertificate(certificatePath, privateKeyPath)
	if err != nil {
		fatal(err.Error())
	}
	listen := env("CONTROL_LISTEN", ":443")
	tlsConfig := server.TLSConfig()
	tlsConfig.GetCertificate = certificate.GetCertificate
	httpServer := &http.Server{Addr: listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, TLSConfig: tlsConfig}
	go func() {
		<-ctx.Done()
		certificateManager.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("control plane listening", "address", listen)
	if err := httpServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		fatal(err.Error())
	}
}

// publishAll rebuilds edge configurations after a renderer upgrade without exposing an HTTP endpoint.
func publishAll() {
	dataDir := env("CONTROL_DATA_DIR", "/var/lib/cdn-platform")
	key := os.Getenv("CONTROL_ENCRYPTION_KEY")
	if key == "" {
		fatal("CONTROL_ENCRYPTION_KEY is required")
	}
	cipher, err := control.NewCipher(key)
	if err != nil {
		fatal(err.Error())
	}
	database, err := store.Open(filepath.Join(dataDir, "control.db"))
	if err != nil {
		fatal("open database: " + err.Error())
	}
	defer database.Close()
	if err := (control.Publisher{Store: database, Cipher: cipher}).PublishAll(); err != nil {
		fatal("publish all sites: " + err.Error())
	}
}

func writeCloudflareCredentials(path string) {
	token := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN"))
	dataDir := env("CONTROL_DATA_DIR", "/var/lib/cdn-platform")
	ciphertext, err := store.ReadSecret(filepath.Join(dataDir, "control.db"), store.SecretCloudflareAPIToken)
	if err == nil {
		key := os.Getenv("CONTROL_ENCRYPTION_KEY")
		if key == "" {
			fatal("CONTROL_ENCRYPTION_KEY is required to decrypt the Cloudflare API token")
		}
		cipher, err := control.NewCipher(key)
		if err != nil {
			fatal(err.Error())
		}
		plaintext, err := cipher.Decrypt(ciphertext)
		if err != nil {
			fatal("decrypt Cloudflare API token: " + err.Error())
		}
		token = strings.TrimSpace(string(plaintext))
	} else if !errors.Is(err, store.ErrNotFound) {
		fatal("read Cloudflare API token: " + err.Error())
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		fatal("CLOUDFLARE_API_TOKEN is not configured")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".cloudflare-credentials-*")
	if err != nil {
		fatal("create Cloudflare credentials: " + err.Error())
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		fatal("secure Cloudflare credentials: " + err.Error())
	}
	if _, err := fmt.Fprintf(temporary, "dns_cloudflare_api_token = %s\n", token); err != nil {
		temporary.Close()
		fatal("write Cloudflare credentials: " + err.Error())
	}
	if err := temporary.Close(); err != nil {
		fatal("close Cloudflare credentials: " + err.Error())
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		fatal("install Cloudflare credentials: " + err.Error())
	}
}

func settingsFromEnv() (control.EnvironmentSettings, error) {
	recipients := split(os.Getenv("SMTP_TO"))
	security := strings.ToLower(env("SMTP_SECURITY", integrations.SMTPSecurityStartTLS))
	if security != integrations.SMTPSecurityStartTLS && security != integrations.SMTPSecurityTLS {
		if len(recipients) != 0 {
			return control.EnvironmentSettings{}, fmt.Errorf("SMTP_SECURITY must be starttls or tls")
		}
		security = integrations.SMTPSecurityStartTLS
	}
	port := 587
	if security == integrations.SMTPSecurityTLS {
		port = 465
	}
	if value := strings.TrimSpace(os.Getenv("SMTP_PORT")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			if len(recipients) != 0 {
				return control.EnvironmentSettings{}, fmt.Errorf("SMTP_PORT must be between 1 and 65535")
			}
		} else {
			port = parsed
		}
	}
	return control.EnvironmentSettings{
		CloudflareAPIToken: os.Getenv("CLOUDFLARE_API_TOKEN"),
		SMTP:               control.SMTPProfile{Enabled: len(recipients) != 0, Host: os.Getenv("SMTP_HOST"), Port: port, Username: os.Getenv("SMTP_USER"), FromAddress: os.Getenv("SMTP_FROM"), Recipients: recipients, Security: security},
		SMTPPassword:       os.Getenv("SMTP_PASSWORD"),
	}, nil
}
func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func split(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
func parseCIDRs(value string) ([]*net.IPNet, error) {
	var result []*net.IPNet
	for _, item := range split(value) {
		_, cidr, err := net.ParseCIDR(item)
		if err != nil {
			return nil, err
		}
		result = append(result, cidr)
	}
	return result, nil
}
func fatal(message string) { fmt.Fprintln(os.Stderr, "cdn-control:", message); os.Exit(1) }
