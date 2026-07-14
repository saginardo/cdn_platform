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
	for _, expected := range []string{"proxy_cache_path /opt/cdn-edge/cache levels=1:2 keys_zone=cdn_cache:100m inactive=7d max_size=5g use_temp_path=off", "client_max_body_size 128m;", "ssl_certificate /opt/cdn-edge/config/certs/site-1.crt", "access_log /opt/cdn-edge/logs/access.json cdn_json", "proxy_cache_lock on", "proxy_cache_background_update on", "proxy_cache_use_stale error timeout", "upstream origin_site-1_primary", "upstream origin_site-1_backup", "proxy_ssl_name origin.example.test", "proxy_ssl_name backup.example.test", "proxy_set_header Host backup.example.test", "proxy_set_header Connection \"\";", "location @cdn_backup_site-1", "site-1:7:$scheme$host$request_uri", "location = /__cdn_health"} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from config:\n%s", expected, configuration)
		}
	}
	if got := strings.Count(configuration, "proxy_set_header Connection \"\";"); got != 2 {
		t.Fatalf("expected Connection header to be cleared in both regular proxy locations, got %d:\n%s", got, configuration)
	}
	if strings.Contains(configuration, "max_size=50g") {
		t.Fatalf("configuration still uses the retired 50g default:\n%s", configuration)
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
	for _, unexpected := range []string{"$cdn_site_id", "proxy_cache_path", "log_format cdn_json", "client_max_body_size"} {
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

func TestRenderAddsStreamingLocationsForWebSocketAndSSE(t *testing.T) {
	configuration, err := Render([]domain.Site{{
		ID: "site-1", Name: "streaming", Domains: []string{"stream.example.test"},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		StreamPaths:   []string{"/events", "/ws"}, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"map $http_upgrade $cdn_connection_upgrade",
		`map $http_upgrade $cdn_connection_upgrade { default upgrade; "" ""; }`,
		"location = /events",
		"location ^~ /events/",
		"location = /ws",
		"proxy_set_header Upgrade $http_upgrade",
		"proxy_set_header Connection $cdn_connection_upgrade",
		"proxy_cache off",
		"proxy_buffering off",
		"proxy_request_buffering off",
		"proxy_read_timeout 1h",
		"proxy_send_timeout 1h",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("missing %q from streaming config:\n%s", expected, configuration)
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
		"location @cdn_backup_site-1 {",
		"proxy_cache off;",
		"proxy_buffering off;",
		"proxy_request_buffering off;",
		"proxy_read_timeout 1h;",
		"proxy_send_timeout 1h;",
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
	if got := strings.Count(configuration, "proxy_cache off;"); got != 2 {
		t.Fatalf("expected cache to be disabled in primary and backup locations, got %d:\n%s", got, configuration)
	}
	if got := strings.Count(configuration, "proxy_set_header Range $http_range;"); got != 2 {
		t.Fatalf("expected Range forwarding in primary and backup locations, got %d:\n%s", got, configuration)
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
		"listen 443 ssl http2",
		"grpc_pass grpcs://origin_grpc-site",
		"grpc_set_header TE trailers",
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
}
