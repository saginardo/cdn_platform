package nginx

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var nginxTestTempPaths = []struct {
	directive string
	directory string
}{
	{directive: "client_body_temp_path", directory: "client-body"},
	{directive: "proxy_temp_path", directory: "proxy"},
	{directive: "fastcgi_temp_path", directory: "fastcgi"},
	{directive: "uwsgi_temp_path", directory: "uwsgi"},
	{directive: "scgi_temp_path", directory: "scgi"},
}

func buildIsolatedNginxConfiguration(t *testing.T, directory, preamble, errorLog, httpConfiguration string) string {
	t.Helper()
	tempRoot := filepath.Join(directory, "nginx-temp")
	var tempDirectives strings.Builder
	for _, item := range nginxTestTempPaths {
		path := filepath.Join(tempRoot, item.directory)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create isolated Nginx temp path %s: %v", path, err)
		}
		fmt.Fprintf(&tempDirectives, "%s %s;\n", item.directive, strconv.Quote(path))
	}

	var configuration strings.Builder
	if preamble != "" {
		configuration.WriteString(preamble)
		if !strings.HasSuffix(preamble, "\n") {
			configuration.WriteByte('\n')
		}
	}
	if os.Geteuid() == 0 {
		configuration.WriteString("user root;\n")
	}
	fmt.Fprintf(&configuration, "pid %s;\n", strconv.Quote(filepath.Join(directory, "nginx.pid")))
	if errorLog == "stderr" {
		configuration.WriteString("error_log stderr notice;\n")
	} else {
		fmt.Fprintf(&configuration, "error_log %s notice;\n", strconv.Quote(errorLog))
	}
	configuration.WriteString("events {}\nhttp {\n")
	configuration.WriteString(tempDirectives.String())
	configuration.WriteString(httpConfiguration)
	if !strings.HasSuffix(httpConfiguration, "\n") {
		configuration.WriteByte('\n')
	}
	configuration.WriteString("}\n")
	return configuration.String()
}

func TestBuildIsolatedNginxConfigurationContainsAllRuntimePaths(t *testing.T) {
	directory := t.TempDir()
	configuration := buildIsolatedNginxConfiguration(t, directory, "", "stderr", "access_log off;")
	if strings.Contains(configuration, "/var/lib/nginx") {
		t.Fatalf("test Nginx configuration references a system temp path:\n%s", configuration)
	}
	for _, item := range nginxTestTempPaths {
		path := filepath.Join(directory, "nginx-temp", item.directory)
		expected := item.directive + " " + strconv.Quote(path) + ";"
		if !strings.Contains(configuration, expected) {
			t.Errorf("test Nginx configuration does not contain %q:\n%s", expected, configuration)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("isolated temp path %s: %v", path, err)
			continue
		}
		if mode := info.Mode().Perm(); mode != 0o700 {
			t.Errorf("isolated temp path %s mode = %04o, want 0700", path, mode)
		}
	}
}
