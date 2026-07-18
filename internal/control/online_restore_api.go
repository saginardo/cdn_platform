package control

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

type startOnlineRestoreRequest struct {
	SnapshotID   string `json:"snapshot_id"`
	Confirmation string `json:"confirmation"`
}

type commitOnlineRestoreRequest struct {
	Confirmation string `json:"confirmation"`
}

func (s *Server) listBackupSnapshots(response http.ResponseWriter, request *http.Request) {
	if s.OnlineRestore == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("online restore is unavailable"))
		return
	}
	snapshots, err := s.OnlineRestore.ListSnapshots(request.Context())
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, snapshots)
}

func (s *Server) currentOnlineRestore(response http.ResponseWriter, _ *http.Request) {
	if s.OnlineRestore == nil {
		writeJSON(response, http.StatusOK, nil)
		return
	}
	writeJSON(response, http.StatusOK, s.OnlineRestore.Current())
}

func (s *Server) startOnlineRestore(response http.ResponseWriter, request *http.Request) {
	if s.OnlineRestore == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("online restore is unavailable"))
		return
	}
	var input startOnlineRestoreRequest
	if !readJSON(response, request, &input) {
		return
	}
	job, err := s.OnlineRestore.Start(input.SnapshotID, input.Confirmation)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already active") {
			status = http.StatusConflict
		}
		writeError(response, status, err)
		return
	}
	writeJSON(response, http.StatusAccepted, job)
}

func (s *Server) commitOnlineRestore(response http.ResponseWriter, request *http.Request) {
	if s.OnlineRestore == nil || s.RestartControl == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("online restore cutover is unavailable"))
		return
	}
	var input commitOnlineRestoreRequest
	if !readJSON(response, request, &input) {
		return
	}
	job, err := s.OnlineRestore.Commit(request.PathValue("id"), input.Confirmation)
	if err != nil {
		writeError(response, http.StatusConflict, err)
		return
	}
	writeJSON(response, http.StatusAccepted, job)
	restart := s.RestartControl
	go func() {
		timer := time.NewTimer(750 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		restart()
	}()
}

func (s *Server) cancelOnlineRestore(response http.ResponseWriter, request *http.Request) {
	if s.OnlineRestore == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("online restore is unavailable"))
		return
	}
	job, err := s.OnlineRestore.Cancel(request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusConflict, err)
		return
	}
	writeJSON(response, http.StatusOK, job)
}
