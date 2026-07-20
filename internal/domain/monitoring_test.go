package domain

import (
	"math"
	"testing"
	"time"
)

func TestNormalizeMonitoringAddress(t *testing.T) {
	tests := map[string]string{
		" EXAMPLE.com:443 ": "example.com:443",
		"203.0.113.9:80":    "203.0.113.9:80",
		"[2001:db8::1]:53":  "[2001:db8::1]:53",
		"localhost:8080":    "localhost:8080",
	}
	for input, wanted := range tests {
		actual, err := NormalizeMonitoringAddress(input)
		if err != nil || actual != wanted {
			t.Fatalf("NormalizeMonitoringAddress(%q) = %q, %v; want %q", input, actual, err, wanted)
		}
	}
	for _, input := range []string{"example.com", "https://example.com:443", "-bad.example:443", "0.0.0.0:80", "example.com:0", "example.com:65536"} {
		if _, err := NormalizeMonitoringAddress(input); err == nil {
			t.Fatalf("NormalizeMonitoringAddress(%q) succeeded", input)
		}
	}
}

func TestValidMonitoringProbeResult(t *testing.T) {
	valid := MonitoringProbeResult{TargetID: "target", Attempts: 3, SuccessfulAttempts: 2, AverageLatencyMS: 12.5, Error: "timeout", CheckedAt: time.Now()}
	if !ValidMonitoringProbeResult(valid) {
		t.Fatal("valid monitoring result was rejected")
	}
	for name, mutate := range map[string]func(*MonitoringProbeResult){
		"no attempts":          func(result *MonitoringProbeResult) { result.Attempts = 0 },
		"too many successes":   func(result *MonitoringProbeResult) { result.SuccessfulAttempts = 4 },
		"zero success latency": func(result *MonitoringProbeResult) { result.SuccessfulAttempts = 0 },
		"NaN latency":          func(result *MonitoringProbeResult) { result.AverageLatencyMS = math.NaN() },
		"control error":        func(result *MonitoringProbeResult) { result.Error = "timeout\nretry" },
	} {
		t.Run(name, func(t *testing.T) {
			result := valid
			mutate(&result)
			if ValidMonitoringProbeResult(result) {
				t.Fatalf("invalid result accepted: %#v", result)
			}
		})
	}
}
