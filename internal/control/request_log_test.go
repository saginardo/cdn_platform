package control

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRequestLogIncludesResponseOutcome(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		level   string
		message string
	}{
		{name: "success", status: http.StatusOK, level: "INFO", message: "msg=request"},
		{name: "client error", status: http.StatusBadRequest, level: "WARN", message: `msg="request rejected"`},
		{name: "server error", status: http.StatusServiceUnavailable, level: "ERROR", message: `msg="request failed"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			server := &Server{Logger: slog.New(slog.NewTextHandler(&output, nil))}
			handler := server.withRequestLog(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte("outcome"))
			}))
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/test", nil)
			handler.ServeHTTP(response, request)

			line := output.String()
			for _, expected := range []string{
				"level=" + test.level,
				test.message,
				"method=POST",
				"path=/api/test",
				"status=" + strconv.Itoa(test.status),
				"response_bytes=7",
				"duration=",
			} {
				if !strings.Contains(line, expected) {
					t.Fatalf("request log %q does not contain %q", line, expected)
				}
			}
		})
	}
}
