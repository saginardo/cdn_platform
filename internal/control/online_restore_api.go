package control

import (
	"errors"
	"net/http"
	"time"
)

type startOnlineRestoreRequest struct {
	SnapshotID   string `json:"snapshot_id"`
	Confirmation string `json:"confirmation"`
}

type commitOnlineRestoreRequest struct {
	Confirmation string `json:"confirmation"`
}

type deleteBackupSnapshotRequest struct {
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

func (s *Server) deleteBackupSnapshot(response http.ResponseWriter, request *http.Request) {
	if s.OnlineRestore == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("online restore is unavailable"))
		return
	}
	var input deleteBackupSnapshotRequest
	if !readJSON(response, request, &input) {
		return
	}
	snapshot, err := s.OnlineRestore.DeleteSnapshot(request.Context(), request.PathValue("id"), input.Confirmation)
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, errResticSnapshotID), errors.Is(err, errResticSnapshotConfirmation):
			status = http.StatusBadRequest
		case errors.Is(err, errOnlineRestoreActive):
			status = http.StatusConflict
		case errors.Is(err, errOnlineRestoreSnapshotMissing):
			status = http.StatusNotFound
		}
		writeError(response, status, err)
		return
	}
	s.audit(request, adminID(request.Context()), "delete_backup_snapshot", "backup_snapshot", snapshot.ID, "short_id="+snapshot.ShortID)
	writeJSON(response, http.StatusOK, map[string]string{"deleted_snapshot_id": snapshot.ID})
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
		if errors.Is(err, errOnlineRestoreActive) {
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
