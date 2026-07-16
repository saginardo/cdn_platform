package domain

import "testing"

func TestClientMaxBodySizeDefaultsAndPresetValidation(t *testing.T) {
	newSite := func(value int) Site {
		return Site{
			Name:                "example",
			Domains:             []string{"example.test"},
			Nodes:               []string{"node-1"},
			PrimaryOrigin:       Origin{URL: "https://origin.example.test"},
			ClientMaxBodySizeMB: value,
		}
	}

	defaultSite := newSite(0)
	if err := NormalizeAndValidateSite(&defaultSite); err != nil {
		t.Fatal(err)
	}
	if defaultSite.ClientMaxBodySizeMB != DefaultClientMaxBodySizeMB {
		t.Fatalf("default client max body size = %d", defaultSite.ClientMaxBodySizeMB)
	}

	for _, value := range []int{128, 256, 512, 1024} {
		site := newSite(value)
		if err := NormalizeAndValidateSite(&site); err != nil {
			t.Fatalf("expected %d MiB to be accepted: %v", value, err)
		}
	}
	for _, value := range []int{-1, 1, 127, 129, 1025} {
		site := newSite(value)
		if err := NormalizeAndValidateSite(&site); err == nil {
			t.Fatalf("expected %d MiB to be rejected", value)
		}
	}
}

func TestReadWriteTimeoutDefaultsAndPresetValidation(t *testing.T) {
	newSite := func(value int) Site {
		return Site{
			Name:                    "example",
			Domains:                 []string{"example.test"},
			Nodes:                   []string{"node-1"},
			PrimaryOrigin:           Origin{URL: "https://origin.example.test"},
			ReadWriteTimeoutSeconds: value,
		}
	}

	defaultSite := newSite(0)
	if err := NormalizeAndValidateSite(&defaultSite); err != nil {
		t.Fatal(err)
	}
	if defaultSite.ReadWriteTimeoutSeconds != DefaultReadWriteTimeoutSeconds {
		t.Fatalf("default read/write timeout = %d", defaultSite.ReadWriteTimeoutSeconds)
	}

	for _, value := range []int{360, 900, 1800, 3600} {
		site := newSite(value)
		if err := NormalizeAndValidateSite(&site); err != nil {
			t.Fatalf("expected %d seconds to be accepted: %v", value, err)
		}
	}
	for _, value := range []int{-1, 1, 359, 361, 7200} {
		site := newSite(value)
		if err := NormalizeAndValidateSite(&site); err == nil {
			t.Fatalf("expected %d seconds to be rejected", value)
		}
	}
}

func TestSiteDNSTTLValidation(t *testing.T) {
	newSite := func(value *int) Site {
		return Site{Name: "example", Domains: []string{"example.test"}, Nodes: []string{"node-1"}, PrimaryOrigin: Origin{URL: "https://origin.example.test"}, DNSTTLSeconds: value}
	}
	inherited := newSite(nil)
	if err := NormalizeAndValidateSite(&inherited); err != nil {
		t.Fatal(err)
	}
	for _, value := range []int{60, 61, 180, 300} {
		ttl := value
		site := newSite(&ttl)
		if err := NormalizeAndValidateSite(&site); err != nil {
			t.Fatalf("expected TTL %d to be accepted: %v", value, err)
		}
	}
	for _, value := range []int{-1, 0, 59, 301} {
		ttl := value
		site := newSite(&ttl)
		if err := NormalizeAndValidateSite(&site); err == nil {
			t.Fatalf("expected TTL %d to be rejected", value)
		}
	}
}

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

func TestOriginSupportsIndependentTLSServerName(t *testing.T) {
	for _, scheme := range []string{"https", "wss", "grpcs"} {
		origin := Origin{URL: scheme + "://203.0.113.8:443", HostHeader: "lax.dustvm.de", TLSServerName: " LAX.DUSTVM.DE ", Enabled: true}
		if err := ValidateOrigin(&origin); err != nil {
			t.Fatalf("expected %s origin TLS server name to be accepted: %v", scheme, err)
		}
		if origin.TLSServerName != "lax.dustvm.de" {
			t.Fatalf("TLS server name was not normalized: %q", origin.TLSServerName)
		}
	}
}

func TestOriginRejectsInvalidTLSServerName(t *testing.T) {
	for _, value := range []string{"203.0.113.8", "lax.dustvm.de:443", "https://lax.dustvm.de", "*.dustvm.de", "bad name"} {
		origin := Origin{URL: "https://203.0.113.8:443", TLSServerName: value, Enabled: true}
		if err := ValidateOrigin(&origin); err == nil {
			t.Fatalf("expected TLS server name %q to be rejected", value)
		}
	}
	for _, scheme := range []string{"http", "ws", "grpc"} {
		origin := Origin{URL: scheme + "://origin.example.test", TLSServerName: "origin.example.test", Enabled: true}
		if err := ValidateOrigin(&origin); err == nil {
			t.Fatalf("expected TLS server name on %s origin to be rejected", scheme)
		}
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

func TestStreamPathsAreRetiredForAPICompatibility(t *testing.T) {
	site := Site{
		Name:          "streaming",
		Domains:       []string{"stream.example.test"},
		Nodes:         []string{"node-1"},
		PrimaryOrigin: Origin{URL: "https://origin.example.test"},
		StreamPaths:   []string{"/bad path", "/../ws"},
	}
	if err := NormalizeAndValidateSite(&site); err != nil {
		t.Fatal(err)
	}
	if site.StreamPaths == nil || len(site.StreamPaths) != 0 {
		t.Fatalf("retired stream paths should normalize to an empty array: %#v", site.StreamPaths)
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
