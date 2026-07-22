package edge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"simple_cdn/internal/nginx"
)

func TestReconcileCacheLayoutRemovesOnlyRetiredManagedDirectories(t *testing.T) {
	root := t.TempDir()
	active := "0123456789abcdef"
	retired := "fedcba9876543210"
	for _, directory := range []string{
		filepath.Join(root, "a", "legacy"),
		filepath.Join(root, "sites", active),
		filepath.Join(root, "sites", retired),
		filepath.Join(root, "sites", "operator-files"),
	} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	configuration := "proxy_cache_path " + nginx.DefaultCachePath + "/sites/" + active + " levels=1:2;\n"
	if err := reconcileCacheLayout(root, configuration); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "sites", active), filepath.Join(root, "sites", "operator-files")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved directory %s: %v", path, err)
		}
	}
	for _, path := range []string{filepath.Join(root, "a"), filepath.Join(root, "sites", retired)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired directory %s remains: %v", path, err)
		}
	}
}

func TestPrepareManagedCacheLayoutCreatesActiveDirectoriesWithRootMode(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	configuration := "proxy_cache_path " + nginx.DefaultCachePath + "/sites/0123456789abcdef levels=1:2;\n"
	if err := prepareManagedCacheLayout(root, configuration); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "sites"), filepath.Join(root, "sites", "0123456789abcdef")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o750 {
			t.Fatalf("%s mode = %o", path, got)
		}
	}
}

func TestActiveCacheDirectoryNamesRejectsUnmanagedPaths(t *testing.T) {
	configuration := "proxy_cache_path " + nginx.DefaultCachePath + "/sites/0123456789abcdef levels=1:2;\n" +
		"proxy_cache_path " + nginx.DefaultCachePath + "/sites/not-managed levels=1:2;\n" +
		"proxy_cache_path /tmp/0123456789abcdef levels=1:2;\n"
	active := activeCacheDirectoryNames(configuration)
	if len(active) != 1 {
		t.Fatalf("active cache directories = %#v", active)
	}
	if _, found := active["0123456789abcdef"]; !found {
		t.Fatalf("expected managed cache directory: %#v", active)
	}
}

func TestInstalledCacheReconciliationRequiresLayoutMarker(t *testing.T) {
	edgeRoot := t.TempDir()
	root := filepath.Join(edgeRoot, "cache")
	retired := filepath.Join(root, "sites", "fedcba9876543210")
	if err := os.MkdirAll(retired, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := reconcileInstalledCacheLayout(root, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(retired); err != nil {
		t.Fatalf("cache changed without a layout marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(edgeRoot, ".layout-version"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileInstalledCacheLayout(root, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(retired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired cache remains with a layout marker: %v", err)
	}
}
