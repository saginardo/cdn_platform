package domain

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func NormalizeAndValidateSite(site *Site) error {
	site.Name = strings.TrimSpace(site.Name)
	if site.Name == "" || len(site.Name) > 100 {
		return fmt.Errorf("site name must be between 1 and 100 characters")
	}
	clientMaxBodySizeMB, err := NormalizeClientMaxBodySizeMB(site.ClientMaxBodySizeMB)
	if err != nil {
		return err
	}
	site.ClientMaxBodySizeMB = clientMaxBodySizeMB
	readWriteTimeoutSeconds, err := NormalizeReadWriteTimeoutSeconds(site.ReadWriteTimeoutSeconds)
	if err != nil {
		return err
	}
	site.ReadWriteTimeoutSeconds = readWriteTimeoutSeconds
	if site.DNSTTLSeconds != nil {
		if err := ValidateDNSTTLSeconds(*site.DNSTTLSeconds); err != nil {
			return err
		}
	}
	if len(site.Domains) == 0 {
		return fmt.Errorf("at least one domain is required")
	}
	seenDomains := make(map[string]struct{}, len(site.Domains))
	domains := make([]string, 0, len(site.Domains))
	for _, domainName := range site.Domains {
		domainName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domainName), "."))
		if net.ParseIP(domainName) != nil {
			return fmt.Errorf("site domain %q must be a DNS hostname, not an IP address", domainName)
		}
		if !ValidHostname(domainName) {
			return fmt.Errorf("invalid domain %q", domainName)
		}
		if _, found := seenDomains[domainName]; found {
			return fmt.Errorf("duplicate domain %q", domainName)
		}
		seenDomains[domainName] = struct{}{}
		domains = append(domains, domainName)
	}
	site.Domains = domains
	if len(site.Nodes) == 0 {
		return fmt.Errorf("at least one node is required")
	}
	seenNodes := make(map[string]struct{}, len(site.Nodes))
	nodes := make([]string, 0, len(site.Nodes))
	for _, nodeID := range site.Nodes {
		nodeID = strings.TrimSpace(nodeID)
		if nodeID == "" {
			return fmt.Errorf("node ID is required")
		}
		if _, found := seenNodes[nodeID]; found {
			return fmt.Errorf("duplicate node ID %q", nodeID)
		}
		seenNodes[nodeID] = struct{}{}
		nodes = append(nodes, nodeID)
	}
	site.Nodes = nodes
	if err := ValidateOrigin(&site.PrimaryOrigin); err != nil {
		return fmt.Errorf("primary origin: %w", err)
	}
	primary, _ := url.Parse(site.PrimaryOrigin.URL)
	if site.Passthrough && primary.Scheme != "http" && primary.Scheme != "https" {
		return fmt.Errorf("passthrough mode is only supported for HTTP and HTTPS origins")
	}
	// Path-specific streaming was retired. Keep the API field stable while making
	// old values inert and consistently returning an empty JSON array.
	site.StreamPaths = []string{}
	if site.BackupOrigin != nil {
		if err := ValidateOrigin(site.BackupOrigin); err != nil {
			return fmt.Errorf("backup origin: %w", err)
		}
		backup, _ := url.Parse(site.BackupOrigin.URL)
		if primary.Scheme != backup.Scheme {
			return fmt.Errorf("primary and backup origin must use the same scheme")
		}
	}
	return nil
}

func NormalizeClientMaxBodySizeMB(value int) (int, error) {
	if value == 0 {
		value = DefaultClientMaxBodySizeMB
	}
	if err := ValidateClientMaxBodySizeMB(value); err != nil {
		return 0, err
	}
	return value, nil
}

func ValidateClientMaxBodySizeMB(value int) error {
	switch value {
	case DefaultClientMaxBodySizeMB, 256, 512, MaxClientMaxBodySizeMB:
		return nil
	default:
		return fmt.Errorf("client max body size must be one of 128, 256, 512, or 1024 MiB")
	}
}

func NormalizeReadWriteTimeoutSeconds(value int) (int, error) {
	if value == 0 {
		value = DefaultReadWriteTimeoutSeconds
	}
	if err := ValidateReadWriteTimeoutSeconds(value); err != nil {
		return 0, err
	}
	return value, nil
}

func ValidateReadWriteTimeoutSeconds(value int) error {
	switch value {
	case 360, 900, 1800, 3600:
		return nil
	default:
		return fmt.Errorf("read/write timeout must be one of 360, 900, 1800, or 3600 seconds")
	}
}

func ValidateDNSTTLSeconds(value int) error {
	if value < MinDNSTTLSeconds || value > MaxDNSTTLSeconds {
		return fmt.Errorf("DNS TTL must be between %d and %d seconds", MinDNSTTLSeconds, MaxDNSTTLSeconds)
	}
	return nil
}

func ValidateOrigin(origin *Origin) error {
	origin.URL = strings.TrimSpace(origin.URL)
	parsed, err := url.Parse(origin.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("must be an absolute HTTP(S), WebSocket, or gRPC URL")
	}
	if !ValidOriginScheme(parsed.Scheme) {
		return fmt.Errorf("scheme must be http, https, ws, wss, grpc, or grpcs")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("must not include credentials, path, query, or fragment")
	}
	if !ValidHostname(parsed.Hostname()) {
		return fmt.Errorf("invalid origin hostname")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return fmt.Errorf("invalid origin port")
		}
	}
	origin.HostHeader = strings.TrimSpace(origin.HostHeader)
	if origin.HostHeader == "" {
		origin.HostHeader = parsed.Hostname()
	}
	if !ValidHostHeader(origin.HostHeader) {
		return fmt.Errorf("invalid origin Host header")
	}
	origin.TLSServerName = strings.ToLower(strings.TrimSpace(origin.TLSServerName))
	if origin.TLSServerName != "" {
		if !OriginUsesTLS(parsed.Scheme) {
			return fmt.Errorf("TLS server name is only supported for HTTPS, WSS, or GRPCS origins")
		}
		if net.ParseIP(origin.TLSServerName) != nil || !ValidHostname(origin.TLSServerName) {
			return fmt.Errorf("invalid TLS server name; use a DNS hostname without a port or wildcard")
		}
	}
	return nil
}

func ValidOriginScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "ws", "wss", "grpc", "grpcs":
		return true
	default:
		return false
	}
}

func IsGRPCScheme(scheme string) bool { return scheme == "grpc" || scheme == "grpcs" }

func IsWebSocketScheme(scheme string) bool { return scheme == "ws" || scheme == "wss" }

func OriginUsesTLS(scheme string) bool {
	return scheme == "https" || scheme == "wss" || scheme == "grpcs"
}

func ProxyScheme(scheme string) string {
	switch scheme {
	case "ws":
		return "http"
	case "wss":
		return "https"
	default:
		return scheme
	}
}

func ValidHostname(value string) bool {
	if net.ParseIP(value) != nil {
		return true
	}
	if len(value) == 0 || len(value) > 253 || strings.Contains(value, "..") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}

func ValidHostHeader(value string) bool {
	if strings.ContainsAny(value, " \t\r\n/@?#") {
		return false
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		if !ValidHostname(strings.Trim(host, "[]")) {
			return false
		}
		parsed, err := strconv.Atoi(port)
		return err == nil && parsed >= 1 && parsed <= 65535
	}
	return ValidHostname(value)
}
