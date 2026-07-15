package control

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallEdgeScriptSyntax(t *testing.T) {
	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(bootstrapEdgeScript)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, output)
	}
}

func TestInstallEdgeScriptCreatesOptLayout(t *testing.T) {
	harness := newInstallHarness(t)
	harness.write("etc/nginx/sites-enabled/default", "default site\n")

	output, err := harness.run(t, "first-token", "edge-binary-v1", "")
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, output)
	}
	for _, path := range []string{
		"opt/cdn-edge/.layout-version",
		"opt/cdn-edge/bin/cdn-edge-agent",
		"opt/cdn-edge/config/edge.env",
		"opt/cdn-edge/data/edge-client.key",
		"opt/cdn-edge/data/edge-client.crt",
		"opt/cdn-edge/data/edge-ca.crt",
		"opt/cdn-edge/systemd/cdn-edge-agent.service",
	} {
		harness.requirePath(t, path)
	}
	harness.requireAbsent(t, "etc/nginx/sites-enabled/default")
	harness.requireLink(t, "etc/nginx/conf.d/cdn-platform.conf", "opt/cdn-edge/config/nginx/cdn-platform.conf")
	harness.requireLink(t, "etc/systemd/system/cdn-edge-agent.service", "opt/cdn-edge/systemd/cdn-edge-agent.service")
	harness.requireContents(t, "opt/cdn-edge/bin/cdn-edge-agent", "edge-binary-v1")
	if configuration := harness.read(t, "opt/cdn-edge/config/nginx/cdn-platform.conf"); !strings.Contains(configuration, "location = /__cdn_health") {
		t.Fatalf("fresh install did not create the health-only Nginx configuration:\n%s", configuration)
	}
	environment := harness.read(t, "opt/cdn-edge/config/edge.env")
	for _, expected := range []string{
		"CONTROL_URL=https://edge-control.example.test",
		"ENROLLMENT_TOKEN=first-token",
		"EDGE_STATE_DIR=/opt/cdn-edge/data",
		"NGINX_CONFIG_PATH=/opt/cdn-edge/config/nginx/cdn-platform.conf",
		"EDGE_CERT_DIR=/opt/cdn-edge/config/certs",
		"EDGE_ACCESS_LOG=/opt/cdn-edge/logs/access.json",
	} {
		if !strings.Contains(environment, expected) {
			t.Fatalf("edge.env does not contain %q:\n%s", expected, environment)
		}
	}
}

func TestInstallEdgeScriptRequiresTokenForFreshHost(t *testing.T) {
	harness := newInstallHarness(t)
	output, err := harness.run(t, "", "edge-binary-v1", "")
	if err == nil || !strings.Contains(output, "an enrollment token is required") {
		t.Fatalf("fresh install without a token was not rejected: %v\n%s", err, output)
	}
	harness.requireAbsent(t, "opt/cdn-edge")
}

func TestInstallEdgeScriptMigratesLegacyStateWithoutCache(t *testing.T) {
	harness := newInstallHarness(t)
	harness.seedLegacy(t)

	output, err := harness.run(t, "", "edge-binary-v2", "")
	if err != nil {
		t.Fatalf("migration failed: %v\n%s", err, output)
	}
	for _, path := range []string{
		"usr/local/bin/cdn-edge-agent", "etc/cdn-platform", "var/lib/cdn-platform",
		"var/log/cdn-platform", "var/cache/cdn-platform",
	} {
		harness.requireAbsent(t, path)
	}
	for path, contents := range map[string]string{
		"opt/cdn-edge/data/edge-client.key":         "legacy-key\n",
		"opt/cdn-edge/data/access-log-queue.ndjson": "queued\n",
		"opt/cdn-edge/data/access-log-offset":       "17\n",
		"opt/cdn-edge/config/certs/site.crt":        "site-cert\n",
		"opt/cdn-edge/logs/access.json":             "access event\n",
		"opt/cdn-edge/logs/access.json.1":           "rotated event\n",
	} {
		harness.requireContents(t, path, contents)
	}
	harness.requireAbsent(t, "opt/cdn-edge/cache/cache-object")
	configuration := harness.read(t, "opt/cdn-edge/config/nginx/cdn-platform.conf")
	for _, expected := range []string{"/opt/cdn-edge/cache", "/opt/cdn-edge/config/certs/site.crt", "/opt/cdn-edge/logs/access.json"} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("migrated Nginx configuration does not contain %q:\n%s", expected, configuration)
		}
	}
	if strings.Contains(configuration, "/var/cache/cdn-platform") || strings.Contains(configuration, "/etc/cdn-platform") || strings.Contains(configuration, "/var/log/cdn-platform") {
		t.Fatalf("migrated Nginx configuration retained legacy paths:\n%s", configuration)
	}
	if environment := harness.read(t, "opt/cdn-edge/config/edge.env"); !strings.Contains(environment, "EDGE_POLL_SECONDS=45") {
		t.Fatalf("migration did not retain poll interval:\n%s", environment)
	}
	log := harness.read(t, "mock.log")
	if !strings.Contains(log, "systemctl restart nginx.service") {
		t.Fatalf("legacy migration did not cold-start the new cache zone:\n%s", log)
	}
}

func TestInstallEdgeScriptUpgradesNewLayoutIdempotently(t *testing.T) {
	harness := newInstallHarness(t)
	if output, err := harness.run(t, "first-token", "edge-binary-v1", ""); err != nil {
		t.Fatalf("first install failed: %v\n%s", err, output)
	}
	harness.write("opt/cdn-edge/data/pending-state", "keep data\n")
	harness.write("opt/cdn-edge/cache/cache-object", "keep new cache\n")
	environmentPath := filepath.Join(harness.root, "opt/cdn-edge/config/edge.env")
	environment := strings.ReplaceAll(harness.read(t, "opt/cdn-edge/config/edge.env"), "EDGE_POLL_SECONDS=30", "EDGE_POLL_SECONDS=75")
	if err := os.WriteFile(environmentPath, []byte(environment), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := harness.run(t, "", "edge-binary-v2", "")
	if err != nil {
		t.Fatalf("upgrade failed: %v\n%s", err, output)
	}
	harness.requireContents(t, "opt/cdn-edge/bin/cdn-edge-agent", "edge-binary-v2")
	harness.requireContents(t, "opt/cdn-edge/data/pending-state", "keep data\n")
	harness.requireContents(t, "opt/cdn-edge/cache/cache-object", "keep new cache\n")
	environment = harness.read(t, "opt/cdn-edge/config/edge.env")
	if !strings.Contains(environment, "EDGE_POLL_SECONDS=75") || !strings.Contains(environment, "ENROLLMENT_TOKEN=\n") {
		t.Fatalf("upgrade did not update environment safely:\n%s", environment)
	}
	log := harness.read(t, "mock.log")
	if !strings.Contains(log, "systemctl reload nginx.service") {
		t.Fatalf("new-layout upgrade did not retain zero-downtime reload:\n%s", log)
	}
}

func TestInstallEdgeScriptRestoresLegacyNginxAfterRestartFailure(t *testing.T) {
	harness := newInstallHarness(t)
	harness.seedLegacy(t)

	output, err := harness.run(t, "", "edge-binary-v2", "nginx-restart-once")
	if err == nil {
		t.Fatalf("migration unexpectedly succeeded:\n%s", output)
	}
	harness.requireAbsent(t, "opt/cdn-edge")
	harness.requireContents(t, "etc/nginx/conf.d/cdn-platform.conf", legacyNginxConfiguration)
	log := harness.read(t, "mock.log")
	if strings.Count(log, "systemctl restart nginx.service") < 2 {
		t.Fatalf("rollback did not cold-start the restored legacy configuration:\n%s", log)
	}
}

func TestInstallEdgeScriptRollsBackLegacyMigration(t *testing.T) {
	harness := newInstallHarness(t)
	harness.seedLegacy(t)

	output, err := harness.run(t, "", "edge-binary-v2", "nginx")
	if err == nil {
		t.Fatalf("migration unexpectedly succeeded:\n%s", output)
	}
	harness.requireAbsent(t, "opt/cdn-edge")
	for path, contents := range map[string]string{
		"usr/local/bin/cdn-edge-agent":              "legacy-binary\n",
		"etc/cdn-platform/edge.env":                 "CONTROL_URL=https://old.example.test\nENROLLMENT_TOKEN=old-token\nEDGE_POLL_SECONDS=45\n",
		"var/lib/cdn-platform/edge-client.key":      "legacy-key\n",
		"var/log/cdn-platform/access.json":          "access event\n",
		"etc/nginx/conf.d/cdn-platform.conf":        legacyNginxConfiguration,
		"etc/systemd/system/cdn-edge-agent.service": "legacy service\n",
		"etc/nginx/sites-enabled/default":           "default site\n",
	} {
		harness.requireContents(t, path, contents)
	}
	if !strings.Contains(harness.read(t, "mock.log"), "systemctl start cdn-edge-agent.service") {
		t.Fatalf("rollback did not restore the legacy service state:\n%s", harness.read(t, "mock.log"))
	}
	matches, globErr := filepath.Glob(filepath.Join(harness.root, "tmp/cdn-edge-install.*"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("installer left transaction directories after rollback: %#v, %v", matches, globErr)
	}
}

func TestInstallEdgeScriptRejectsMixedLayouts(t *testing.T) {
	harness := newInstallHarness(t)
	harness.write("opt/cdn-edge/.layout-version", "1\n")
	harness.write("opt/cdn-edge/bin/cdn-edge-agent", "new binary\n")
	harness.write("var/lib/cdn-platform/edge-client.key", "legacy key\n")

	output, err := harness.run(t, "token", "edge-binary-v2", "")
	if err == nil || !strings.Contains(output, "both /opt/cdn-edge and legacy CDN paths exist") {
		t.Fatalf("mixed layout was not rejected: %v\n%s", err, output)
	}
}

const legacyNginxConfiguration = `# Generated by cdn-edge-agent. Do not edit.
proxy_cache_path /var/cache/cdn-platform levels=1:2 keys_zone=cdn_cache:100m;
server {
    listen 80 default_server;
    location = /__cdn_health { return 200 "ok\n"; }
    ssl_certificate /etc/cdn-platform/certs/site.crt;
    access_log /var/log/cdn-platform/access.json;
}
`

type installHarness struct {
	root        string
	mockBin     string
	logPath     string
	binaryPath  string
	servicePath string
}

func newInstallHarness(t *testing.T) *installHarness {
	t.Helper()
	root := t.TempDir()
	harness := &installHarness{
		root:        root,
		mockBin:     filepath.Join(root, "mock-bin"),
		logPath:     filepath.Join(root, "mock.log"),
		binaryPath:  filepath.Join(root, "download-binary"),
		servicePath: filepath.Join(root, "download-service"),
	}
	for _, directory := range []string{"tmp", "run", "mock-bin", "etc/nginx/conf.d", "etc/nginx/sites-enabled", "etc/systemd/system"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(harness.servicePath, []byte(bootstrapEdgeService), 0o644); err != nil {
		t.Fatal(err)
	}
	harness.writeMock(t, "curl", `#!/usr/bin/env bash
set -euo pipefail
printf 'curl %s\n' "$*" >>"$MOCK_LOG"
output=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
if [[ -z "$output" ]]; then exit 0; fi
case "$url" in
  https://downloads.example.test/edge) cp "$MOCK_BINARY" "$output" ;;
  https://edge-control.example.test/install-edge.service) cp "$MOCK_SERVICE" "$output" ;;
  *) echo "unexpected URL: $url" >&2; exit 1 ;;
esac
`)
	harness.writeMock(t, "nginx", `#!/usr/bin/env bash
printf 'nginx %s\n' "$*" >>"$MOCK_LOG"
if [[ "${MOCK_FAILURE:-}" == "nginx" && "${1:-}" == "-t" ]]; then exit 1; fi
exit 0
`)
	harness.writeMock(t, "sha256sum", `#!/usr/bin/env bash
set -euo pipefail
read -r expected path
actual=$(shasum -a 256 "$path" | awk '{print $1}')
[[ "$expected" == "$actual" ]]
`)
	harness.writeMock(t, "sleep", "#!/usr/bin/env bash\nexit 0\n")
	harness.writeMock(t, "systemctl", `#!/usr/bin/env bash
set -euo pipefail
printf 'systemctl %s\n' "$*" >>"$MOCK_LOG"
root="$CDN_EDGE_INSTALL_ROOT"
active="$root/run/mock-agent-active"
enabled="$root/run/mock-agent-enabled"
nginx_active="$root/run/mock-nginx-active"
command="${1:-}"
service="${*: -1}"
case "$command" in
  is-active)
    if [[ "$service" == "nginx.service" ]]; then [[ -f "$nginx_active" ]]; exit; fi
    [[ -f "$active" ]]
    ;;
  is-enabled) [[ -f "$enabled" ]] ;;
  stop)
    if [[ "$service" == "cdn-edge-agent.service" ]]; then rm -f "$active"; fi
    if [[ "$service" == "nginx.service" ]]; then rm -f "$nginx_active"; fi
    ;;
  disable) rm -f "$enabled" ;;
  enable) touch "$enabled" ;;
  start|restart)
    if [[ "$service" == "nginx.service" ]]; then
      if [[ "${MOCK_FAILURE:-}" == "nginx-restart-once" && ! -f "$root/run/mock-nginx-restart-failed" ]]; then
        touch "$root/run/mock-nginx-restart-failed"
        exit 1
      fi
      touch "$nginx_active"
    fi
    if [[ "$service" == "cdn-edge-agent.service" ]]; then
      if [[ "${MOCK_FAILURE:-}" == "agent" ]]; then exit 1; fi
      touch "$active"
      unit="$root/etc/systemd/system/cdn-edge-agent.service"
      if [[ -L "$unit" && "$(readlink "$unit")" == "$root/opt/cdn-edge/systemd/cdn-edge-agent.service" ]]; then
        mkdir -p "$root/opt/cdn-edge/data"
        for file in edge-client.key edge-client.crt edge-ca.crt; do
          [[ -s "$root/opt/cdn-edge/data/$file" ]] || printf '%s\n' "$file" >"$root/opt/cdn-edge/data/$file"
        done
      fi
    fi
    ;;
  reload)
    if [[ "${MOCK_FAILURE:-}" == "reload" && "$service" == "nginx" ]]; then exit 1; fi
    ;;
esac
exit 0
`)
	return harness
}

func (h *installHarness) seedLegacy(t *testing.T) {
	t.Helper()
	for path, contents := range map[string]string{
		"usr/local/bin/cdn-edge-agent":                 "legacy-binary\n",
		"etc/cdn-platform/edge.env":                    "CONTROL_URL=https://old.example.test\nENROLLMENT_TOKEN=old-token\nEDGE_POLL_SECONDS=45\n",
		"etc/cdn-platform/certs/site.crt":              "site-cert\n",
		"etc/cdn-platform/certs/site.key":              "site-key\n",
		"var/lib/cdn-platform/edge-client.key":         "legacy-key\n",
		"var/lib/cdn-platform/edge-client.crt":         "legacy-cert\n",
		"var/lib/cdn-platform/edge-ca.crt":             "legacy-ca\n",
		"var/lib/cdn-platform/applied-version":         "9\n",
		"var/lib/cdn-platform/access-log-queue.ndjson": "queued\n",
		"var/lib/cdn-platform/access-log-offset":       "17\n",
		"var/log/cdn-platform/access.json":             "access event\n",
		"var/log/cdn-platform/access.json.1":           "rotated event\n",
		"var/cache/cdn-platform/cache-object":          "discard cache\n",
		"etc/nginx/conf.d/cdn-platform.conf":           legacyNginxConfiguration,
		"etc/nginx/sites-enabled/default":              "default site\n",
		"etc/systemd/system/cdn-edge-agent.service":    "legacy service\n",
	} {
		h.write(path, contents)
	}
	h.write("run/mock-agent-active", "")
	h.write("run/mock-agent-enabled", "")
	h.write("run/mock-nginx-active", "")
}

func (h *installHarness) run(t *testing.T, token, binary, failure string) (string, error) {
	t.Helper()
	if err := os.WriteFile(h.binaryPath, []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(binary)))
	arguments := []string{"-s", "--", "--control-url", "https://edge-control.example.test"}
	if token != "" {
		arguments = append(arguments, "--enrollment-token", token)
	}
	arguments = append(arguments, "--binary-url", "https://downloads.example.test/edge", "--binary-sha256", digest)
	command := exec.Command("bash", arguments...)
	command.Stdin = strings.NewReader(bootstrapEdgeScript)
	command.Env = []string{
		"PATH=" + h.mockBin + ":/usr/bin:/bin",
		"CDN_EDGE_INSTALL_ROOT=" + h.root,
		"MOCK_LOG=" + h.logPath,
		"MOCK_BINARY=" + h.binaryPath,
		"MOCK_SERVICE=" + h.servicePath,
		"MOCK_FAILURE=" + failure,
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func (h *installHarness) writeMock(t *testing.T, name, contents string) {
	t.Helper()
	path := filepath.Join(h.mockBin, name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (h *installHarness) write(path, contents string) {
	fullPath := filepath.Join(h.root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
		panic(err)
	}
}

func (h *installHarness) read(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(h.root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func (h *installHarness) requireContents(t *testing.T, path, expected string) {
	t.Helper()
	if actual := h.read(t, path); actual != expected {
		t.Fatalf("%s = %q, want %q", path, actual, expected)
	}
}

func (h *installHarness) requirePath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(h.root, path)); err != nil {
		t.Fatalf("expected %s: %v", path, err)
	}
}

func (h *installHarness) requireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(h.root, path)); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be absent: %v", path, err)
	}
}

func (h *installHarness) requireLink(t *testing.T, path, target string) {
	t.Helper()
	actual, err := os.Readlink(filepath.Join(h.root, path))
	if err != nil {
		t.Fatalf("read %s symlink: %v", path, err)
	}
	expected := filepath.Join(h.root, target)
	if actual != expected {
		t.Fatalf("%s -> %s, want %s", path, actual, expected)
	}
}
