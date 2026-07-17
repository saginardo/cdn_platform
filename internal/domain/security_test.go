package domain

import "testing"

func TestDefaultSecurityPolicyPatternAndDurations(t *testing.T) {
	matcher, err := CompileSecurityPattern(DefaultSecurityPolicyPattern)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/.env", "/app/.env.production", "/.git/config", "/.AWS/credentials", "/.htpasswd"} {
		if !matcher.MatchString(path) {
			t.Errorf("default policy did not match %q", path)
		}
	}
	for _, path := range []string{"/", "/assets/app.js", "/environment", "/git/config"} {
		if matcher.MatchString(path) {
			t.Errorf("default policy unexpectedly matched %q", path)
		}
	}
	for _, seconds := range []int{3600, 21600, 43200, 86400} {
		if !ValidSecurityBanDuration(seconds) {
			t.Errorf("duration %d is not accepted", seconds)
		}
	}
	if ValidSecurityBanDuration(7200) {
		t.Fatal("unsupported duration was accepted")
	}
}

func TestNormalizeSecurityPolicy(t *testing.T) {
	policy, err := NormalizeSecurityPolicy(SecurityPolicy{
		Name: " sensitive files ", Enabled: true, Pattern: DefaultSecurityPolicyPattern,
		Action: SecurityActionBan, BanDurationSeconds: 21600, Priority: 100,
	})
	if err != nil || policy.Name != "sensitive files" {
		t.Fatalf("normalized policy = %#v, err=%v", policy, err)
	}
	if _, err := NormalizeSecurityPolicy(SecurityPolicy{Name: "bad", Pattern: `(?=lookahead)`, Action: SecurityActionBlock, Priority: 1}); err == nil {
		t.Fatal("unsupported regular expression was accepted")
	}
	if _, err := NormalizeSecurityPolicy(SecurityPolicy{Name: "variable", Pattern: `^/$request_uri`, Action: SecurityActionBlock, Priority: 1}); err == nil {
		t.Fatal("Nginx variable reference was accepted")
	}
	for _, pattern := range []string{`^/(foo)$1`, `^/price\$`} {
		if _, err := NormalizeSecurityPolicy(SecurityPolicy{Name: "dollar", Pattern: pattern, Action: SecurityActionBlock, Priority: 1}); err == nil {
			t.Fatalf("unsafe dollar pattern %q was accepted", pattern)
		}
	}
	if _, err := NormalizeSecurityPolicy(SecurityPolicy{Name: "backtracking", Pattern: `^/(a+)+$`, Action: SecurityActionBlock, Priority: 1}); err == nil {
		t.Fatal("potentially unsafe repeated group was accepted")
	}
	if _, err := NormalizeSecurityPolicy(SecurityPolicy{Name: "safe", Pattern: `(?i)^/+wp-admin(?:/.*|$)`, Action: SecurityActionBlock, Priority: 1}); err != nil {
		t.Fatalf("safe custom pattern was rejected: %v", err)
	}
}
