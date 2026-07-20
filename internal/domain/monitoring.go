package domain

import (
	"errors"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const MaxMonitoringTargets = 32

type MonitoringTarget struct {
	ID        string    `json:"id"`
	Address   string    `json:"address"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MonitoringProbeResult struct {
	TargetID           string    `json:"target_id"`
	Attempts           int       `json:"attempts"`
	SuccessfulAttempts int       `json:"successful_attempts"`
	AverageLatencyMS   float64   `json:"average_latency_ms"`
	Error              string    `json:"error,omitempty"`
	CheckedAt          time.Time `json:"checked_at"`
}

func NormalizeMonitoringAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 300 {
		return "", errors.New("monitoring address must be host:port")
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil || host == "" {
		return "", errors.New("monitoring address must be IP:port or domain:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("monitoring port must be between 1 and 65535")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return "", errors.New("monitoring IP address is not reachable")
		}
		return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !validMonitoringDomain(host) {
		return "", errors.New("monitoring host must be a valid IP address or domain")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func ValidMonitoringProbeResult(result MonitoringProbeResult) bool {
	if strings.TrimSpace(result.TargetID) == "" || len(result.TargetID) > 64 || result.TargetID != strings.TrimSpace(result.TargetID) ||
		result.Attempts < 1 || result.Attempts > 10 || result.SuccessfulAttempts < 0 || result.SuccessfulAttempts > result.Attempts ||
		math.IsNaN(result.AverageLatencyMS) || math.IsInf(result.AverageLatencyMS, 0) || result.AverageLatencyMS < 0 || result.AverageLatencyMS > 60_000 ||
		result.CheckedAt.IsZero() || len(result.Error) > 512 || result.Error != strings.TrimSpace(result.Error) {
		return false
	}
	if result.SuccessfulAttempts == 0 && result.AverageLatencyMS != 0 || result.SuccessfulAttempts > 0 && result.AverageLatencyMS <= 0 {
		return false
	}
	for _, character := range result.Error {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validMonitoringDomain(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}
