package edge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"cdn-platform/internal/nginx"
)

func prepareManagedCacheLayout(root, configuration string) error {
	active := activeCacheDirectoryNames(configuration)
	if len(active) == 0 {
		return nil
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("inspect cache root: %w", err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("cache root is not a directory")
	}
	stat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect cache root ownership")
	}
	paths := make([]string, 0, len(active)+1)
	paths = append(paths, filepath.Join(root, "sites"))
	for name := range active {
		paths = append(paths, filepath.Join(root, "sites", name))
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, rootInfo.Mode().Perm()); err != nil {
			return fmt.Errorf("create managed cache directory %s: %w", path, err)
		}
		if err := os.Chown(path, int(stat.Uid), int(stat.Gid)); err != nil {
			return fmt.Errorf("set managed cache directory ownership %s: %w", path, err)
		}
		if err := os.Chmod(path, rootInfo.Mode().Perm()); err != nil {
			return fmt.Errorf("set managed cache directory permissions %s: %w", path, err)
		}
	}
	return nil
}

func reconcileInstalledCacheLayout(root, configuration string) error {
	marker := filepath.Join(filepath.Dir(root), ".layout-version")
	info, err := os.Stat(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("edge layout marker is not a regular file")
	}
	return reconcileCacheLayout(root, configuration)
}

func reconcileCacheLayout(root, configuration string) error {
	active := activeCacheDirectoryNames(configuration)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cleanupErrors []error
	for _, entry := range entries {
		if entry.IsDir() && isLegacyCacheDirectoryName(entry.Name()) {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	sitesRoot := filepath.Join(root, "sites")
	entries, err = os.ReadDir(sitesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return errors.Join(cleanupErrors...)
	}
	if err != nil {
		cleanupErrors = append(cleanupErrors, err)
		return errors.Join(cleanupErrors...)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isManagedCacheDirectoryName(entry.Name()) {
			continue
		}
		if _, keep := active[entry.Name()]; keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(sitesRoot, entry.Name())); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func activeCacheDirectoryNames(configuration string) map[string]struct{} {
	active := make(map[string]struct{})
	prefix := nginx.DefaultCachePath + "/sites/"
	for _, line := range strings.Split(configuration, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "proxy_cache_path" || !strings.HasPrefix(fields[1], prefix) {
			continue
		}
		name := strings.TrimPrefix(fields[1], prefix)
		if isManagedCacheDirectoryName(name) {
			active[name] = struct{}{}
		}
	}
	return active
}

func isLegacyCacheDirectoryName(name string) bool {
	return len(name) == 1 && strings.ContainsRune("0123456789abcdef", rune(name[0]))
}

func isManagedCacheDirectoryName(name string) bool {
	if len(name) != 16 {
		return false
	}
	for _, character := range name {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
