package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cdn-platform/internal/domain"
)

func TestSecurityPolicyAndBanLifecycle(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	policies, err := database.ListSecurityPolicies()
	if err != nil || len(policies) != 1 || !policies[0].Builtin || policies[0].ID != domain.DefaultSecurityPolicyID {
		t.Fatalf("seeded policies = %#v, err=%v", policies, err)
	}
	policy := policies[0]
	policy.BanDurationSeconds = 3600
	updated, err := database.UpdateSecurityPolicy(policy.ID, policy)
	if err != nil || updated.BanDurationSeconds != 3600 || !updated.Builtin {
		t.Fatalf("updated policy = %#v, err=%v", updated, err)
	}
	if err := database.DeleteSecurityPolicy(policy.ID); err == nil {
		t.Fatal("built-in policy was deleted")
	}
	node, err := database.CreateNode("security-edge", "203.0.113.77")
	if err != nil {
		t.Fatal(err)
	}
	event := domain.SecurityEvent{
		ID: "11111111-1111-4111-8111-111111111111", PolicyID: policy.ID, ClientIP: "8.8.8.8", Host: "cdn.example.test", Path: "/.env",
		Method: "GET", Action: domain.SecurityActionBan, BanDurationSeconds: 3600, ObservedAt: time.Now().UTC(),
	}
	accepted, err := database.RecordSecurityEvents(node.ID, []domain.SecurityEvent{event})
	if err != nil || accepted != 1 {
		t.Fatalf("record event accepted=%d, err=%v", accepted, err)
	}
	bans, err := database.ListActiveSecurityBans()
	if err != nil || len(bans) != 1 || bans[0].IP != "8.8.8.8" || bans[0].TriggerNodeID != node.ID {
		t.Fatalf("active bans = %#v, err=%v", bans, err)
	}
	if remaining := time.Until(bans[0].ExpiresAt); remaining < 59*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("unexpected ban duration %s", remaining)
	}
	events, err := database.ListRecentSecurityEvents(10)
	if err != nil || len(events) != 1 || events[0].PolicyName != policy.Name {
		t.Fatalf("recent events = %#v, err=%v", events, err)
	}
	if err := database.DeleteSecurityBan("8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	if bans, err := database.ListActiveSecurityBans(); err != nil || len(bans) != 0 {
		t.Fatalf("bans after delete = %#v, err=%v", bans, err)
	}
	bad := event
	bad.ID = "22222222-2222-4222-8222-222222222222"
	bad.Path = "/assets/app.js"
	if _, err := database.RecordSecurityEvents(node.ID, []domain.SecurityEvent{bad}); err == nil {
		t.Fatal("event whose path did not match the policy was accepted")
	} else {
		var inputError *SecurityEventInputError
		if !errors.As(err, &inputError) || inputError.Index != 0 {
			t.Fatalf("invalid event error = %#v", err)
		}
	}
	stale := event
	stale.ID = "55555555-5555-4555-8555-555555555555"
	stale.ClientIP = "9.9.9.9"
	stale.ObservedAt = time.Now().UTC().Add(-2 * time.Hour)
	if accepted, err := database.RecordSecurityEvents(node.ID, []domain.SecurityEvent{stale}); err != nil || accepted != 1 {
		t.Fatalf("stale event accepted=%d, err=%v", accepted, err)
	}
	if bans, err := database.ListActiveSecurityBans(); err != nil || len(bans) != 0 {
		t.Fatalf("stale event created a fresh ban: %#v, err=%v", bans, err)
	}
}

func TestCustomSecurityPolicyCRUD(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	created, err := database.CreateSecurityPolicy(domain.SecurityPolicy{
		Name: "PHP probes", Enabled: true, Pattern: `(?i)^/+wp-admin(?:/|$)`,
		Action: domain.SecurityActionBlock, Priority: 200,
	})
	if err != nil || created.ID == "" || created.Builtin {
		t.Fatalf("created policy = %#v, err=%v", created, err)
	}
	if err := database.DeleteSecurityPolicy(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SecurityPolicy(created.ID); err != ErrNotFound {
		t.Fatalf("deleted policy lookup = %v", err)
	}
}

func TestSecurityBanListingLimitAndCount(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("security-edge", "203.0.113.88")
	if err != nil {
		t.Fatal(err)
	}
	policies, err := database.ListSecurityPolicies()
	if err != nil {
		t.Fatal(err)
	}
	events := []domain.SecurityEvent{
		{ID: "33333333-3333-4333-8333-333333333333", PolicyID: policies[0].ID, ClientIP: "8.8.8.8", Path: "/.env", Action: domain.SecurityActionBan, ObservedAt: time.Now().UTC()},
		{ID: "44444444-4444-4444-8444-444444444444", PolicyID: policies[0].ID, ClientIP: "1.1.1.1", Path: "/.git/config", Action: domain.SecurityActionBan, ObservedAt: time.Now().UTC()},
	}
	if accepted, err := database.RecordSecurityEvents(node.ID, events); err != nil || accepted != len(events) {
		t.Fatalf("record events accepted=%d, err=%v", accepted, err)
	}
	count, err := database.CountActiveSecurityBans()
	if err != nil || count != 2 {
		t.Fatalf("active ban count=%d, err=%v", count, err)
	}
	bans, err := database.ListActiveSecurityBansLimit(1)
	if err != nil || len(bans) != 1 {
		t.Fatalf("limited bans=%#v, err=%v", bans, err)
	}
	if accepted, err := database.RecordSecurityEvents(node.ID, events); err != nil || accepted != len(events) {
		t.Fatalf("replay accepted=%d, err=%v", accepted, err)
	}
	recent, err := database.ListRecentSecurityEvents(10)
	if err != nil || len(recent) != len(events) {
		t.Fatalf("events after idempotent replay=%#v, err=%v", recent, err)
	}
}
