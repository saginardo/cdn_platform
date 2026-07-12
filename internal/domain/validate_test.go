package domain

import "testing"

func TestSiteDomainRejectsIPAddress(t *testing.T) {
	site := Site{
		Name:    "example",
		Domains: []string{"203.0.113.7"},
		Nodes:   []string{"node-1"},
		PrimaryOrigin: Origin{
			URL:     "https://203.0.113.8",
			Enabled: true,
		},
	}
	if err := NormalizeAndValidateSite(&site); err == nil {
		t.Fatal("expected an IP address to be rejected as a site domain")
	}
}

func TestOriginMayUseIPAddress(t *testing.T) {
	origin := Origin{URL: "https://203.0.113.8:8443", Enabled: true}
	if err := ValidateOrigin(&origin); err != nil {
		t.Fatalf("expected an IP origin to remain supported: %v", err)
	}
}

func TestOriginSupportsWebSocketAndGRPCSchemes(t *testing.T) {
	for _, value := range []string{"ws://origin.example.test:8080", "wss://origin.example.test", "grpc://grpc.example.test:50051", "grpcs://grpc.example.test"} {
		origin := Origin{URL: value}
		if err := ValidateOrigin(&origin); err != nil {
			t.Fatalf("expected %q to be accepted: %v", value, err)
		}
	}
}

func TestStreamPathsAreNormalizedAndRestrictedForGRPC(t *testing.T) {
	site := Site{
		Name:          "streaming",
		Domains:       []string{"stream.example.test"},
		Nodes:         []string{"node-1"},
		PrimaryOrigin: Origin{URL: "https://origin.example.test"},
		StreamPaths:   []string{"/events/", "/ws"},
	}
	if err := NormalizeAndValidateSite(&site); err != nil {
		t.Fatal(err)
	}
	if len(site.StreamPaths) != 2 || site.StreamPaths[0] != "/events" || site.StreamPaths[1] != "/ws" {
		t.Fatalf("unexpected normalized paths: %#v", site.StreamPaths)
	}
	for _, paths := range [][]string{{"/bad path"}, {"/../ws"}, {"/ws?token=1"}} {
		site.StreamPaths = paths
		if err := NormalizeAndValidateSite(&site); err == nil {
			t.Fatalf("expected %v to be rejected", paths)
		}
	}
	site.PrimaryOrigin = Origin{URL: "grpcs://grpc.example.test:443"}
	site.StreamPaths = []string{"/"}
	if err := NormalizeAndValidateSite(&site); err == nil {
		t.Fatal("expected gRPC site stream paths to be rejected")
	}
	site.PrimaryOrigin = Origin{URL: "wss://ws.example.test"}
	if err := NormalizeAndValidateSite(&site); err == nil {
		t.Fatal("expected WebSocket site stream paths to be rejected")
	}
}

func TestPassthroughIsRestrictedToHTTPOrigins(t *testing.T) {
	site := Site{
		Name:          "passthrough",
		Domains:       []string{"stream.example.test"},
		Nodes:         []string{"node-1"},
		PrimaryOrigin: Origin{URL: "https://origin.example.test"},
		Passthrough:   true,
	}
	if err := NormalizeAndValidateSite(&site); err != nil {
		t.Fatalf("HTTP passthrough should be valid: %v", err)
	}
	for _, origin := range []string{"ws://origin.example.test", "grpcs://origin.example.test"} {
		site.PrimaryOrigin = Origin{URL: origin}
		if err := NormalizeAndValidateSite(&site); err == nil {
			t.Fatalf("expected passthrough origin %q to be rejected", origin)
		}
	}
}
