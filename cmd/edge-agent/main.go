package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cdn-platform/internal/edge"
)

func main() {
	pollSeconds := 30
	if value := os.Getenv("EDGE_POLL_SECONDS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 5 || parsed > 300 {
			fatal("EDGE_POLL_SECONDS must be between 5 and 300")
		}
		pollSeconds = parsed
	}
	agent, err := edge.New(edge.Config{
		ControlURL: os.Getenv("CONTROL_URL"), EnrollmentToken: os.Getenv("ENROLLMENT_TOKEN"),
		StateDir: env("EDGE_STATE_DIR", "/opt/cdn-edge/data"), NginxConfigPath: env("NGINX_CONFIG_PATH", "/opt/cdn-edge/config/nginx/cdn-platform.conf"),
		CertificateDir: env("EDGE_CERT_DIR", "/opt/cdn-edge/config/certs"), AccessLogPath: env("EDGE_ACCESS_LOG", "/opt/cdn-edge/logs/access.json"), PollInterval: time.Duration(pollSeconds) * time.Second,
	})
	if err != nil {
		fatal(err.Error())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx); err != nil && err != context.Canceled {
		fatal(err.Error())
	}
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func fatal(message string) {
	log.Print("cdn-edge-agent: " + message)
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
