package version

import (
	"os"
	"strings"
	"testing"
)

func TestVersionMatchesRepositoryVersion(t *testing.T) {
	contents, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if repositoryVersion := strings.TrimSpace(string(contents)); Version != repositoryVersion {
		t.Fatalf("Version = %q, repository VERSION = %q", Version, repositoryVersion)
	}
}
