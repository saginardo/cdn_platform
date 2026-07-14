package domain

import (
	"fmt"
	"net"
	"net/url"
	"sort"
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
	streamPaths, err := NormalizeStreamPaths(site.StreamPaths)
	if err != nil {
		return err
	}
	primary, _ := url.Parse(site.PrimaryOrigin.URL)
	if site.Passthrough && primary.Scheme != "http" && primary.Scheme != "https" {
		return fmt.Errorf("passthrough mode is only supported for HTTP and HTTPS origins")
	}
	if IsGRPCScheme(primary.Scheme) && len(streamPaths) > 0 {
		return fmt.Errorf("stream paths can only be used with HTTP, HTTPS, WS, or WSS origins")
	}
	if IsWebSocketScheme(primary.Scheme) && len(streamPaths) > 0 {
		return fmt.Errorf("WS and WSS origins proxy the entire hostname and must not set stream paths")
	}
	site.StreamPaths = streamPaths
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

func NormalizeStreamPaths(paths []string) ([]string, error) {
	if len(paths) > 20 {
		return nil, fmt.Errorf("at most 20 stream paths are allowed")
	}
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if len(path) > 256 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
			return nil, fmt.Errorf("stream path %q must be an absolute path up to 256 characters", path)
		}
		if path != "/" {
			path = strings.TrimRight(path, "/")
		}
		if path == "" {
			path = "/"
		}
		if strings.Contains(path, "//") {
			return nil, fmt.Errorf("stream path %q must not contain an empty segment", path)
		}
		for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
			if segment == "." || segment == ".." {
				return nil, fmt.Errorf("stream path %q must not contain dot segments", path)
			}
		}
		for _, character := range path {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("/-._~", character)) {
				return nil, fmt.Errorf("stream path %q contains an unsupported character", path)
			}
		}
		if _, exists := seen[path]; exists {
			return nil, fmt.Errorf("duplicate stream path %q", path)
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	sort.Strings(result)
	if len(result) > 1 && result[0] == "/" {
		return nil, fmt.Errorf("stream path / cannot be combined with other stream paths")
	}
	return result, nil
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
