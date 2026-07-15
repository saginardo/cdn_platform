package nginx

import (
	"strings"
	"testing"

	"cdn-platform/internal/domain"
)

func TestRenderIncludesCacheAndFailoverPolicy(t *testing.T) {
	backup := domain.Origin{URL: "https://backup.example.test", Enabled: true}
	configuration, err := Render([]domain.Site{{ID: "site-1", Name: "site", Domains: []string{"cdn.example.test"}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, BackupOrigin: &backup, CacheGeneration: 7, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"proxy_cache_path /opt/cdn-edge/cache levels=1:2 keys_zone=cdn_cache:100m inactive=7d max_size=5g use_temp_path=off", "client_max_body_size 128m;", "keepalive_timeout 300s;", "keepalive_requests 1000;", "keepalive 30;", "proxy_connect_timeout 10s;", "recursive_error_pages on;", "ssl_certificate /opt/cdn-edge/config/certs/site-1.crt", "access_log /opt/cdn-edge/logs/access.json cdn_json", "proxy_cache_lock on", "proxy_cache_background_update on", "proxy_cache_use_stale error timeout", "upstream origin_site-1_primary", "upstream origin_site-1_backup", "proxy_ssl_name origin.example.test", "proxy_ssl_name backup.example.test", "proxy_set_header Host backup.example.test", "proxy_set_header Upgrade \"\";", "proxy_set_header Connection \"\";", "location @cdn_http_site-1", "location @cdn_stream_site-1", "location @cdn_backup_site-1", "location @cdn_stream_backup_site-1", "site-1:7:$scheme$host$request_uri", "location = /__cdn_health", `return 200 "site=site-1\n";`} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from config:\n%s", expected, configuration)
		}
	}
	if !HasSiteHealth(configuration, "site-1") || HasSiteHealth(configuration, "other-site") {
		t.Fatalf("site health capability detection is incorrect:\n%s", configuration)
	}
	if got := strings.Count(configuration, "proxy_set_header Connection \"\";"); got != 2 {
		t.Fatalf("expected Connection header to be cleared in both regular proxy locations, got %d:\n%s", got, configuration)
	}
	if got := strings.Count(configuration, "proxy_set_header Upgrade \"\";"); got != 2 {
		t.Fatalf("expected Upgrade header to be cleared in both regular proxy locations, got %d:\n%s", got, configuration)
	}
	if got := strings.Count(configuration, "keepalive 30;"); got != 2 {
		t.Fatalf("expected one 30-connection pool for each upstream, got %d:\n%s", got, configuration)
	}
	for _, retired := range []string{"keepalive 32;", "proxy_connect_timeout 5s;", "grpc_connect_timeout 5s;", "proxy_read_timeout 60s;"} {
		if strings.Contains(configuration, retired) {
			t.Fatalf("configuration still contains retired connection setting %q:\n%s", retired, configuration)
		}
	}
	if strings.Contains(configuration, "max_size=50g") {
		t.Fatalf("configuration still uses the retired 50g default:\n%s", configuration)
	}
}

func TestRenderUsesConfiguredReadWriteTimeout(t *testing.T) {
	backup := domain.Origin{URL: "https://backup.example.test", Enabled: true}
	configuration, err := Render([]domain.Site{{
		ID: "site-1", Name: "site", Domains: []string{"cdn.example.test"},
		PrimaryOrigin:           domain.Origin{URL: "https://origin.example.test", Enabled: true},
		BackupOrigin:            &backup,
		ReadWriteTimeoutSeconds: 1800,
		Enabled:                 true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(configuration, "proxy_read_timeout 1800s;"); got != 4 {
		t.Fatalf("expected configured read timeout in normal/stream primary/backup locations, got %d:\n%s", got, configuration)
	}
	if got := strings.Count(configuration, "proxy_send_timeout 1800s;"); got != 4 {
		t.Fatalf("expected configured send timeout in normal/stream primary/backup locations, got %d:\n%s", got, configuration)
	}
	for _, retired := range []string{"proxy_read_timeout 60s;", "proxy_read_timeout 1h;", "proxy_send_timeout 1h;"} {
		if strings.Contains(configuration, retired) {
			t.Fatalf("configuration still contains retired HTTP timeout %q:\n%s", retired, configuration)
		}
	}
	defaultConfiguration, err := Render([]domain.Site{{
		ID: "default", Name: "default", Domains: []string{"default.example.test"},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(defaultConfiguration, "proxy_read_timeout 360s;"); got != 2 {
		t.Fatalf("expected the default timeout in regular and stream locations, got %d:\n%s", got, defaultConfiguration)
	}
	if _, err := Render([]domain.Site{{
		ID: "invalid", Name: "invalid", Domains: []string{"invalid.example.test"},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, ReadWriteTimeoutSeconds: 901, Enabled: true,
	}}); err == nil {
		t.Fatal("expected an unsupported read/write timeout to be rejected")
	}
}

func TestRenderUsesConfiguredClientMaxBodySize(t *testing.T) {
	configuration, err := Render([]domain.Site{{
		ID: "site-1", Name: "site", Domains: []string{"api.example.test"},
		PrimaryOrigin:       domain.Origin{URL: "https://origin.example.test", Enabled: true},
		ClientMaxBodySizeMB: 1024, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configuration, "client_max_body_size 1024m;") {
		t.Fatalf("configured client max body size is missing:\n%s", configuration)
	}
	if strings.Contains(configuration, "client_max_body_size 0m;") {
		t.Fatalf("configuration disabled the client body limit:\n%s", configuration)
	}
	if _, err := Render([]domain.Site{{
		ID: "invalid", Name: "invalid", Domains: []string{"invalid.example.test"},
		PrimaryOrigin:       domain.Origin{URL: "https://origin.example.test", Enabled: true},
		ClientMaxBodySizeMB: 129, Enabled: true,
	}}); err == nil {
		t.Fatal("expected an unsupported client max body size to be rejected")
	}
}

func TestRenderEmptyNodeConfigurationDoesNotReferenceSiteVariables(t *testing.T) {
	configuration, err := Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configuration, "location = /__cdn_health") {
		t.Fatalf("empty node configuration lost the health endpoint:\n%s", configuration)
	}
	for _, unexpected := range []string{"$cdn_site_id", "proxy_cache_path", "log_format cdn_json", "client_max_body_size", "keepalive_timeout", "keepalive_requests"} {
		if strings.Contains(configuration, unexpected) {
			t.Fatalf("empty node configuration contains %q:\n%s", unexpected, configuration)
		}
	}
}

func TestRenderRejectsOriginPath(t *testing.T) {
	_, err := Render([]domain.Site{{ID: "site-1", Name: "site", Domains: []string{"cdn.example.test"}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test/not-allowed", Enabled: true}, Enabled: true}})
	if err == nil {
		t.Fatal("expected origin path validation error")
	}
}

func TestRenderOnlyUsesTLSUpstreamDirectivesForHTTPSOrigins(t *testing.T) {
	configuration, err := Render([]domain.Site{{ID: "site-1", Name: "site", Domains: []string{"cdn.example.test"}, PrimaryOrigin: domain.Origin{URL: "http://origin.example.test", Enabled: true}, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configuration, "proxy_ssl_verify on") {
		t.Fatalf("HTTP origin should not emit TLS upstream directives:\n%s", configuration)
	}
}

func TestRenderUsesIndependentTLSServerNamesForIPOrigins(t *testing.T) {
	backup := domain.Origin{URL: "https://203.0.113.21:443", HostHeader: "backup.dustvm.de", TLSServerName: "backup.dustvm.de", Enabled: true}
	configuration, err := Render([]domain.Site{{
		ID: "ip-origin", Name: "ip-origin", Domains: []string{"lax.dustvm.de"},
		PrimaryOrigin: domain.Origin{URL: "https://203.0.113.20:443", HostHeader: "lax.dustvm.de", TLSServerName: "lax.dustvm.de", Enabled: true},
		BackupOrigin:  &backup, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"server 203.0.113.20:443", "server 203.0.113.21:443",
		"proxy_set_header Host lax.dustvm.de", "proxy_set_header Host backup.dustvm.de",
		"proxy_ssl_name lax.dustvm.de", "proxy_ssl_name backup.dustvm.de",
		"proxy_ssl_verify on", "proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from IP origin config:\n%s", expected, configuration)
		}
	}
	if strings.Contains(configuration, "proxy_ssl_name 203.0.113.") {
		t.Fatalf("IP connection address leaked into TLS certificate name:\n%s", configuration)
	}
	if got := strings.Count(configuration, "proxy_ssl_name lax.dustvm.de;"); got != 2 {
		t.Fatalf("expected primary SNI in regular and stream locations, got %d:\n%s", got, configuration)
	}
	if got := strings.Count(configuration, "proxy_ssl_name backup.dustvm.de;"); got != 2 {
		t.Fatalf("expected backup SNI in regular and stream locations, got %d:\n%s", got, configuration)
	}
}

func TestRenderUsesIndependentTLSServerNameForWSSOrigin(t *testing.T) {
	configuration, err := Render([]domain.Site{{
		ID: "wss-ip", Name: "wss-ip", Domains: []string{"ws.dustvm.de"},
		PrimaryOrigin: domain.Origin{URL: "wss://203.0.113.20:443", HostHeader: "ws.dustvm.de", TLSServerName: "ws.dustvm.de", Enabled: true}, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"server 203.0.113.20:443", "proxy_set_header Host ws.dustvm.de", "proxy_ssl_name ws.dustvm.de", "proxy_ssl_verify on"} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from WSS IP origin config:\n%s", expected, configuration)
		}
	}
}

func TestRenderAutomaticallyRoutesWebSocketAndSSEWithoutPaths(t *testing.T) {
	configuration, err := Render([]domain.Site{{
		ID: "site-1", Name: "streaming", Domains: []string{"stream.example.test"},
		PrimaryOrigin:           domain.Origin{URL: "https://origin.example.test", Enabled: true},
		StreamPaths:             []string{"/events", "/ws"},
		ReadWriteTimeoutSeconds: 900,
		Enabled:                 true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"map $http_upgrade $cdn_is_websocket { default 0; ~*^websocket$ 1; }",
		"map $http_accept $cdn_accepts_event_stream",
		"map $http_x_cdn_stream $cdn_forced_stream",
		`map "$request_method:$cdn_is_websocket:$cdn_accepts_event_stream:$cdn_forced_stream" $cdn_auto_stream`,
		"~^POST: 1;",
		"~^[^:]+:1: 1;",
		"~^[^:]+:[01]:1: 1;",
		"~^[^:]+:[01]:[01]:1$ 1;",
		"error_page 418 = @cdn_stream_site-1",
		"error_page 419 = @cdn_http_site-1",
		"if ($cdn_auto_stream) { return 418; }",
		"return 419;",
		"location @cdn_http_site-1",
		"location @cdn_stream_site-1",
		"proxy_set_header Upgrade $cdn_upstream_upgrade",
		"proxy_set_header Connection $cdn_connection_upgrade",
		`proxy_set_header X-CDN-Stream ""`,
		"proxy_cache off",
		"proxy_buffering off",
		"proxy_cache_methods GET HEAD",
		"proxy_connect_timeout 10s",
		"proxy_read_timeout 900s",
		"proxy_send_timeout 900s",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from streaming config:\n%s", expected, configuration)
		}
	}
	for _, retired := range []string{"location = /events", "location ^~ /events/", "location = /ws", "location ^~ /ws/", "proxy_request_buffering off;", "proxy_read_timeout 1h;"} {
		if strings.Contains(configuration, retired) {
			t.Fatalf("automatic streaming config contains retired directive %q:\n%s", retired, configuration)
		}
	}
}

func TestRenderPassthroughDisablesCacheAndForwardsRanges(t *testing.T) {
	backup := domain.Origin{URL: "https://backup.example.test", Enabled: true}
	configuration, err := Render([]domain.Site{{
		ID: "site-1", Name: "passthrough", Domains: []string{"stream.example.test"},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		BackupOrigin:  &backup, Passthrough: true, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"location / {",
		"location @cdn_http_site-1 {",
		"location @cdn_stream_site-1 {",
		"location @cdn_backup_site-1 {",
		"location @cdn_stream_backup_site-1 {",
		"proxy_cache off;",
		"proxy_buffering off;",
		"proxy_request_buffering off;",
		"proxy_connect_timeout 10s;",
		"proxy_read_timeout 360s;",
		"proxy_send_timeout 360s;",
		"proxy_set_header Range $http_range;",
		"proxy_set_header If-Range $http_if_range;",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from passthrough config:\n%s", expected, configuration)
		}
	}
	if strings.Contains(configuration, "proxy_cache cdn_cache;") || strings.Contains(configuration, "proxy_cache_key \"site-1:") {
		t.Fatalf("passthrough site inherited cache configuration:\n%s", configuration)
	}
	if got := strings.Count(configuration, "proxy_cache off;"); got != 4 {
		t.Fatalf("expected cache to be disabled in normal/stream primary/backup locations, got %d:\n%s", got, configuration)
	}
	if got := strings.Count(configuration, "proxy_set_header Range $http_range;"); got != 4 {
		t.Fatalf("expected Range forwarding in normal/stream primary/backup locations, got %d:\n%s", got, configuration)
	}
}

func TestRenderWebSocketOriginRemainsFullyUnbuffered(t *testing.T) {
	configuration, err := Render([]domain.Site{{
		ID: "ws-site", Name: "websocket", Domains: []string{"ws.example.test"},
		PrimaryOrigin: domain.Origin{URL: "wss://origin.example.test", Enabled: true}, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configuration, "proxy_cache cdn_cache;") {
		t.Fatalf("WebSocket origin inherited HTTP cache configuration:\n%s", configuration)
	}
	for _, expected := range []string{"proxy_pass https://origin_ws-site", "proxy_cache off;", "proxy_buffering off;", "proxy_request_buffering off;", "proxy_read_timeout 360s;"} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("WebSocket origin is missing %q:\n%s", expected, configuration)
		}
	}
}

func TestRenderUsesGRPCPassForGRPCOrigin(t *testing.T) {
	configuration, err := Render([]domain.Site{{
		ID: "grpc-site", Name: "grpc", Domains: []string{"grpc.example.test"},
		PrimaryOrigin: domain.Origin{URL: "grpcs://origin.example.test:443", Enabled: true}, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"listen 443 ssl;",
		"http2 on;",
		"grpc_pass grpcs://origin_grpc-site",
		"grpc_set_header TE trailers",
		"grpc_connect_timeout 10s",
		"grpc_read_timeout 1h",
		"grpc_ssl_server_name on",
		"grpc_ssl_name origin.example.test",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from gRPC config:\n%s", expected, configuration)
		}
	}
	if strings.Contains(configuration, "proxy_pass grpcs://") {
		t.Fatalf("gRPC origin must not use proxy_pass:\n%s", configuration)
	}
	if strings.Contains(configuration, "listen 443 ssl http2") {
		t.Fatalf("configuration still uses the deprecated HTTP/2 listen parameter:\n%s", configuration)
	}
}

func TestRenderUsesIndependentTLSServerNamesForGRPCSIPOrigins(t *testing.T) {
	backup := domain.Origin{URL: "grpcs://203.0.113.31:443", HostHeader: "grpc-backup.dustvm.de", TLSServerName: "grpc-backup.dustvm.de", Enabled: true}
	configuration, err := Render([]domain.Site{{
		ID: "grpc-ip", Name: "grpc-ip", Domains: []string{"grpc.dustvm.de"},
		PrimaryOrigin: domain.Origin{URL: "grpcs://203.0.113.30:443", HostHeader: "grpc.dustvm.de", TLSServerName: "grpc.dustvm.de", Enabled: true},
		BackupOrigin:  &backup, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"server 203.0.113.30:443", "server 203.0.113.31:443",
		"grpc_set_header Host grpc.dustvm.de", "grpc_set_header Host grpc-backup.dustvm.de",
		"grpc_ssl_name grpc.dustvm.de", "grpc_ssl_name grpc-backup.dustvm.de",
		"grpc_ssl_verify on", "grpc_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from GRPCS IP origin config:\n%s", expected, configuration)
		}
	}
	if strings.Contains(configuration, "grpc_ssl_name 203.0.113.") {
		t.Fatalf("IP connection address leaked into gRPC TLS certificate name:\n%s", configuration)
	}
}
