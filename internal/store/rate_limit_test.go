package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"cdn-platform/internal/domain"
)

func TestRateLimitPolicyLifecycle(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	policies, err := database.ListRateLimitPolicies()
	if err != nil || len(policies) != 0 {
		t.Fatalf("initial rate limit policies = %#v, err=%v", policies, err)
	}
	created, err := database.CreateRateLimitPolicy(domain.RateLimitPolicy{
		Name: " API errors ", Enabled: true, RequestsPerSecond: 12,
		ResponseConditionEnabled: true, ResponseStatusClasses: []int{5, 4, 5},
		BanEnabled: true, BanAfterConsecutive429: 3, BanDurationSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "API errors" || created.Key != domain.RateLimitKeyClientIP ||
		!slices.Equal(created.ResponseStatusClasses, []int{4, 5}) || !created.BanEnabled ||
		created.BanAfterConsecutive429 != 3 || created.BanDurationSeconds != 3600 || created.CreatedAt.IsZero() {
		t.Fatalf("created rate limit policy = %#v", created)
	}

	loaded, err := database.RateLimitPolicy(created.ID)
	if err != nil || loaded.RequestsPerSecond != 12 || !slices.Equal(loaded.ResponseStatusClasses, []int{4, 5}) || !loaded.BanEnabled {
		t.Fatalf("loaded rate limit policy = %#v, err=%v", loaded, err)
	}
	loaded.Name = "All traffic"
	loaded.RequestsPerSecond = 50
	loaded.ResponseConditionEnabled = false
	loaded.ResponseStatusClasses = []int{2, 3}
	loaded.BanEnabled = false
	updated, err := database.UpdateRateLimitPolicy(loaded.ID, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "All traffic" || updated.RequestsPerSecond != 50 ||
		updated.ResponseConditionEnabled || updated.ResponseStatusClasses != nil || updated.BanEnabled ||
		updated.BanAfterConsecutive429 != 3 || updated.BanDurationSeconds != 3600 || !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Fatalf("updated rate limit policy = %#v", updated)
	}

	if _, err := database.CreateRateLimitPolicy(domain.RateLimitPolicy{Name: "invalid", RequestsPerSecond: 0}); err == nil {
		t.Fatal("invalid rate limit policy was stored")
	}
	if err := database.DeleteRateLimitPolicy(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RateLimitPolicy(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted rate limit policy lookup = %v", err)
	}
	if err := database.DeleteRateLimitPolicy(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second rate limit policy delete = %v", err)
	}
}
