package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"simple_cdn/internal/store"
	"simple_cdn/internal/version"
)

func TestSystemInfoRequiresAdminAndReportsBuildVersion(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.CreateInitialAdmin("hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession("admin", "session-token", "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	handler := (&Server{Store: database}).Handler()
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/system/info", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated system info status = %d", unauthenticated.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/system/info", nil)
	request.AddCookie(&http.Cookie{Name: "cdn_session", Value: "session-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("system info status = %d: %s", response.Code, response.Body.String())
	}
	var info systemInfoResponse
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "simple_cdn" || info.Version != version.Version {
		t.Fatalf("system info = %#v", info)
	}
}
