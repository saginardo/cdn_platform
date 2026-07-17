package nginx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cdn-platform/internal/domain"
)

func defaultSecurityPoliciesForTest() []domain.SecurityPolicy {
	return []domain.SecurityPolicy{
		{
			ID: domain.DefaultSecurityPolicyID, Name: "sensitive", Enabled: true,
			Pattern: domain.DefaultSecurityPolicyPattern, Action: domain.SecurityActionBan,
			BanDurationSeconds: 21600, Priority: 100,
		},
		{
			ID: domain.DefaultPHPSecurityPolicyID, Name: "PHP probes", Enabled: true,
			Pattern: domain.DefaultPHPSecurityPolicyPattern, Action: domain.SecurityActionBlock, Priority: 200,
		},
	}
}

func TestRenderWithSecurityPolicies(t *testing.T) {
	site := domain.Site{
		ID: "site-a", Name: "site-a", Domains: []string{"cdn.example.test"},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}
	configuration, err := RenderWithSecurity([]domain.Site{site}, defaultSecurityPoliciesForTest())
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{
		"# CDN security revision:", "map $uri $cdn_security_policy_id", "log_format cdn_security_json", "security.json cdn_security_json",
		"if ($cdn_security_policy_id) { return 444; }", `"ban"`, `"block"`, "21600", `\\.env`, "php[-_]?info",
	} {
		if !strings.Contains(configuration, wanted) {
			t.Errorf("security configuration lacks %q:\n%s", wanted, configuration)
		}
	}
}

func TestDisabledSecurityPoliciesRetainRevisionMarker(t *testing.T) {
	policies := []domain.SecurityPolicy{{ID: domain.DefaultSecurityPolicyID, Enabled: false}}
	configuration, err := RenderWithSecurity(nil, policies)
	if err != nil {
		t.Fatal(err)
	}
	if !HasSecurityRevision(configuration, policies) || strings.Contains(configuration, "cdn_security_policy_id") {
		t.Fatalf("disabled security policy configuration is not revision-marked:\n%s", configuration)
	}
}

func TestRenderWithoutSecurityRetainsLegacyShape(t *testing.T) {
	configuration, err := Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configuration, "cdn_security") || strings.Contains(configuration, "security.json") {
		t.Fatalf("legacy render unexpectedly contains security configuration:\n%s", configuration)
	}
}

func TestRenderedSecurityConfigurationPassesNginxSyntaxCheck(t *testing.T) {
	binary, err := exec.LookPath("nginx")
	if err != nil {
		t.Skip("nginx is not installed")
	}
	configuration, err := RenderWithSecurity(nil, defaultSecurityPoliciesForTest())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	configuration = strings.ReplaceAll(configuration, "/opt/cdn-edge/logs/security.json", filepath.Join(directory, "security.json"))
	nginxConfiguration := "pid " + filepath.Join(directory, "nginx.pid") + ";\nerror_log stderr;\nevents {}\nhttp {\n" + configuration + "\n}\n"
	path := filepath.Join(directory, "nginx.conf")
	if err := os.WriteFile(path, []byte(nginxConfiguration), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-t", "-c", path, "-p", directory)
	if output, err := command.CombinedOutput(); err != nil && !(strings.Contains(string(output), "syntax is ok") && strings.Contains(string(output), "Operation not permitted")) {
		t.Fatalf("nginx -t: %v\n%s\n%s", err, output, nginxConfiguration)
	}
}
