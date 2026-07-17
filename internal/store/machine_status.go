package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cdn-platform/internal/domain"
)

func (s *Store) RecordNodeMachineStatus(nodeID string, status domain.MachineStatus) error {
	status.CollectedAt = status.CollectedAt.UTC()
	if !domain.ValidMachineStatus(status) {
		return errors.New("invalid node machine status report")
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode node machine status: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO node_machine_status(node_id, status_json, collected_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET status_json = excluded.status_json,
			collected_at = excluded.collected_at, updated_at = excluded.updated_at
		WHERE excluded.collected_at >= node_machine_status.collected_at`,
		nodeID, string(encoded), machineStatusStamp(status.CollectedAt), stamp(now()))
	if err != nil {
		return fmt.Errorf("record node machine status: %w", err)
	}
	return nil
}

func (s *Store) GetNodeMachineStatus(nodeID string) (domain.MachineStatus, error) {
	var encoded string
	err := s.db.QueryRow(`SELECT status_json FROM node_machine_status WHERE node_id = ?`, nodeID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MachineStatus{}, ErrNotFound
	}
	if err != nil {
		return domain.MachineStatus{}, err
	}
	var status domain.MachineStatus
	if err := json.Unmarshal([]byte(encoded), &status); err != nil {
		return domain.MachineStatus{}, fmt.Errorf("decode node machine status: %w", err)
	}
	if !domain.ValidMachineStatus(status) {
		return domain.MachineStatus{}, errors.New("stored node machine status is invalid")
	}
	return status, nil
}

func machineStatusStamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
