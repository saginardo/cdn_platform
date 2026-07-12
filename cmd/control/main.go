package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	cloudflareToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	token := func() (string, error) {
		if cloudflareToken == "" {
			return "", fmt.Errorf("CLOUDFLARE_API_TOKEN is not configured")
		}
		return cloudflareToken, nil
	}
	dns := integrations.CloudflareDNS{Token: token}
	issuer := integrations.CertbotDNSIssuer{Email: os.Getenv("ACME_EMAIL"), ConfigDir: filepath.Join(dataDir, "letsencrypt"), Token: token}
	var logs logstore.Store = logstore.Noop{}
	if os.Getenv("CLICKHOUSE_DISABLED") != "1" {
		clickhouse := logstore.ClickHouse{Endpoint: env("CLICKHOUSE_URL", "http://127.0.0.1:8123"), Database: env("CLICKHOUSE_DATABASE", "cdn_platform"), Username: os.Getenv("CLICKHOUSE_USER"), Password: os.Getenv("CLICKHOUSE_PASSWORD")}
		if err := clickhouse.EnsureSchema(context.Background()); err != nil {
			logger.Warn("ClickHouse schema setup failed; log writes will retry", "error", err)
		}
		logs = clickhouse
	}
	publisher := control.Publisher{Store: database, Cipher: cipher}
	notifier := notifierFromEnv()
	issueTimeout := 10 * time.Minute
	if value := strings.TrimSpace(os.Getenv("CERTIFICATE_ISSUE_TIMEOUT")); value != "" {
		issueTimeout, err = time.ParseDuration(value)
		if err != nil || issueTimeout <= 0 {
			fatal("CERTIFICATE_ISSUE_TIMEOUT must be a positive Go duration")
		}
	}
	certificateManager := &control.CertificateManager{Store: database, Publisher: publisher, Issuer: issuer, Notifier: notifier, IssueTimeout: issueTimeout}
	setupCIDRs, err := parseCIDRs(os.Getenv("SETUP_ALLOW_CIDRS"))
	if err != nil {
		fatal("SETUP_ALLOW_CIDRS: " + err.Error())
	}
	trustedProxyCIDRs, err := parseCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		fatal("TRUSTED_PROXY_CIDRS: " + err.Error())
	}
	controlURL := env("CONTROL_PUBLIC_URL", "https://control.example.invalid")
	server := &control.Server{Store: database, Cipher: cipher, CA: ca, Publisher: publisher, DNS: dns, Issuer: issuer, CertificateManager: certificateManager, Notifier: notifier, Logs: logs, ControlURL: controlURL, EdgeControlURL: env("EDGE_CONTROL_URL", controlURL), EdgeBinaryURL: os.Getenv("EDGE_BINARY_URL"), EdgeBinarySHA256: os.Getenv("EDGE_BINARY_SHA256"), EdgeBinaryPath: os.Getenv("EDGE_BINARY_PATH"), SetupAllowCIDRs: setupCIDRs, TrustedProxyCIDRs: trustedProxyCIDRs, Logger: logger}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	healthManager := &control.HealthManager{Server: server}
	certificateManager.Start(ctx)
	defer certificateManager.Stop()
	go healthManager.Run(ctx)
	go certificateManager.Run(ctx)
	listen := env("CONTROL_LISTEN", ":443")
	httpServer := &http.Server{Addr: listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, TLSConfig: server.TLSConfig()}
	go func() {
		<-ctx.Done()
		certificateManager.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	certificatePath, privateKeyPath := os.Getenv("CONTROL_TLS_CERT"), os.Getenv("CONTROL_TLS_KEY")
	if certificatePath == "" || privateKeyPath == "" {
		fatal("CONTROL_TLS_CERT and CONTROL_TLS_KEY are required")
	}
	logger.Info("control plane listening", "address", listen)
	if err := httpServer.ListenAndServeTLS(certificatePath, privateKeyPath); err != nil && err != http.ErrServerClosed {
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

func notifierFromEnv() integrations.Notifier {
	to := split(os.Getenv("SMTP_TO"))
	if len(to) == 0 {
		return integrations.NoopNotifier{}
	}
	return integrations.SMTPNotifier{Host: os.Getenv("SMTP_HOST"), Port: env("SMTP_PORT", "587"), Username: os.Getenv("SMTP_USER"), Password: os.Getenv("SMTP_PASSWORD"), From: os.Getenv("SMTP_FROM"), To: to, StartTLS: true}
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
