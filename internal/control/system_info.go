package control

import (
	"net/http"

	"simple_cdn/internal/version"
)

type systemInfoResponse struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (s *Server) systemInfo(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, systemInfoResponse{
		Name:    "simple_cdn",
		Version: version.Version,
	})
}
