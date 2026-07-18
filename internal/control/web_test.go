package control

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var viteAssetPattern = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

func TestEmbeddedWebContainsViteApplication(t *testing.T) {
	index := embeddedFile(t, "web/dist/index.html")
	for _, expected := range []string{
		`<html lang="zh-CN">`,
		`<div id="root"></div>`,
		`<script type="module"`,
		`CDN Platform 控制台`,
	} {
		if !strings.Contains(index, expected) {
			t.Fatalf("embedded Vite index is missing %q", expected)
		}
	}

	matches := viteAssetPattern.FindAllStringSubmatch(index, -1)
	if len(matches) < 2 {
		t.Fatalf("embedded Vite index references %d assets, want at least JS and CSS", len(matches))
	}
	for _, match := range matches {
		path := "web/dist" + match[1]
		contents, err := embeddedWeb.ReadFile(path)
		if err != nil {
			t.Fatalf("read embedded asset %s: %v", path, err)
		}
		if len(contents) == 0 {
			t.Fatalf("embedded asset %s is empty", path)
		}
	}
}

func TestLegacyWebFilesAreNotEmbedded(t *testing.T) {
	for _, path := range []string{
		"web/index.html",
		"web/app.js",
		"web/styles.css",
		"web/lucide-icons.svg",
	} {
		_, err := embeddedWeb.ReadFile(path)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("legacy asset %s is still embedded: %v", path, err)
		}
	}
}

func TestHandlerServesViteIndexWithSecurityHeaders(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	(&Server{}).Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", response.Code)
	}
	if !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
		t.Fatal("GET / did not serve the Vite application")
	}
	if got := response.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := response.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	csp := response.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"script-src 'self'",
		"font-src 'self'",
		"connect-src 'self'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("Content-Security-Policy is missing %q: %s", directive, csp)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("script policy unexpectedly permits inline JavaScript: %s", csp)
	}
}

func TestHandlerServesHashedAssetsAsImmutable(t *testing.T) {
	index := embeddedFile(t, "web/dist/index.html")
	matches := viteAssetPattern.FindAllStringSubmatch(index, -1)
	if len(matches) == 0 {
		t.Fatal("Vite index has no asset references")
	}

	for _, match := range matches {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, match[1], nil)
		(&Server{}).Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", match[1], response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("asset %s Cache-Control = %q", match[1], got)
		}
		contentType := response.Header().Get("Content-Type")
		switch {
		case strings.HasSuffix(match[1], ".js") && !strings.Contains(contentType, "javascript"):
			t.Fatalf("asset %s Content-Type = %q", match[1], contentType)
		case strings.HasSuffix(match[1], ".css") && !strings.Contains(contentType, "text/css"):
			t.Fatalf("asset %s Content-Type = %q", match[1], contentType)
		}
	}
}

func embeddedFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := embeddedWeb.ReadFile(path)
	if err != nil {
		t.Fatalf("read embedded file %s: %v", path, err)
	}
	return string(contents)
}
