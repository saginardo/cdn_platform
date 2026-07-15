package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cdn-platform/internal/domain"
	"github.com/google/uuid"
)

const (
	SiteDeletionWithdrawingDNS  = "withdrawing_dns"
	SiteDeletionWaitingForEdges = "waiting_for_edges"
	SiteDeletionFinalizing      = "finalizing"
)

type SiteDeletionJob struct {
	SiteID     string
	TaskID     string
	Phase      string
	Actor      string
	RemoteAddr string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s *Store) BeginSiteDeletion(siteID, actor, remoteAddr string, deadline time.Time) (domain.Site, domain.DeploymentTask, bool, error) {
	site, _, err := s.GetSite(siteID)
	if err != nil {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}
	defer tx.Rollback()

	existing, err := scanTask(tx.QueryRow(`SELECT id, kind, site_id, status, detail, deadline_at, created_at, updated_at
		FROM deployment_tasks WHERE site_id = ? AND kind = 'delete_site' AND status IN (?, ?, ?)
		ORDER BY created_at DESC LIMIT 1`, siteID, domain.TaskQueued, domain.TaskDispatching, domain.TaskApplying))
	if err == nil {
		return site, existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}

	var conflicting int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM deployment_tasks
		WHERE site_id = ? AND kind IN ('publish_site', 'issue_certificate', 'renew_certificate')
		AND status IN (?, ?, ?)`, siteID, domain.TaskQueued, domain.TaskDispatching, domain.TaskApplying).Scan(&conflicting); err != nil {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}
	if conflicting != 0 {
		return domain.Site{}, domain.DeploymentTask{}, false, ErrSiteTaskActive
	}

	created := now()
	task := domain.DeploymentTask{
		ID: uuid.NewString(), Kind: "delete_site", SiteID: siteID, Status: domain.TaskQueued,
		Detail: "queued for safe site deletion", DeadlineAt: &deadline, CreatedAt: created, UpdatedAt: created,
	}
	if _, err := tx.Exec(`INSERT INTO deployment_tasks(id, kind, site_id, status, detail, deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, task.ID, task.Kind, task.SiteID, task.Status, task.Detail,
		stamp(deadline), stamp(created), stamp(created)); err != nil {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}
	result, err := tx.Exec(`UPDATE sites SET enabled = 0, deleting = 1, updated_at = ? WHERE id = ?`, stamp(created), siteID)
	if err != nil {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return domain.Site{}, domain.DeploymentTask{}, false, err
		}
		return domain.Site{}, domain.DeploymentTask{}, false, ErrNotFound
	}
	if _, err := tx.Exec(`INSERT INTO site_deletion_jobs(site_id, task_id, phase, actor, remote_addr, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(site_id) DO UPDATE SET task_id=excluded.task_id, phase=excluded.phase,
		actor=excluded.actor, remote_addr=excluded.remote_addr, updated_at=excluded.updated_at`,
		siteID, task.ID, SiteDeletionWithdrawingDNS, actor, remoteAddr, stamp(created), stamp(created)); err != nil {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO audit_logs(id, actor, action, resource_type, resource_id, remote_addr, detail, created_at)
		VALUES (?, ?, 'delete_requested', 'site', ?, ?, ?, ?)`, uuid.NewString(), actor, siteID, remoteAddr,
		"task="+task.ID+" site="+site.Name, stamp(created)); err != nil {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Site{}, domain.DeploymentTask{}, false, err
	}
	site.Enabled = false
	site.Deleting = true
	site.UpdatedAt = created
	return site, task, true, nil
}

func (s *Store) SiteDeletionJobForTask(taskID string) (SiteDeletionJob, error) {
	return scanSiteDeletionJob(s.db.QueryRow(`SELECT site_id, task_id, phase, actor, remote_addr, created_at, updated_at
		FROM site_deletion_jobs WHERE task_id = ?`, taskID))
}

func (s *Store) ActiveSiteDeletionJobs() ([]SiteDeletionJob, error) {
	rows, err := s.db.Query(`SELECT j.site_id, j.task_id, j.phase, j.actor, j.remote_addr, j.created_at, j.updated_at
		FROM site_deletion_jobs j JOIN deployment_tasks t ON t.id = j.task_id
		WHERE t.status IN (?, ?, ?) ORDER BY j.created_at`, domain.TaskQueued, domain.TaskDispatching, domain.TaskApplying)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]SiteDeletionJob, 0)
	for rows.Next() {
		job, err := scanSiteDeletionJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func scanSiteDeletionJob(row scanner) (SiteDeletionJob, error) {
	var job SiteDeletionJob
	var createdAt, updatedAt string
	err := row.Scan(&job.SiteID, &job.TaskID, &job.Phase, &job.Actor, &job.RemoteAddr, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SiteDeletionJob{}, ErrNotFound
	}
	if err != nil {
		return SiteDeletionJob{}, err
	}
	job.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return SiteDeletionJob{}, err
	}
	job.UpdatedAt, err = parseTime(updatedAt)
	return job, err
}

func (s *Store) SetSiteDeletionPhase(taskID, phase string, status domain.TaskStatus, detail string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updatedAt := stamp(now())
	result, err := tx.Exec(`UPDATE site_deletion_jobs SET phase = ?, updated_at = ? WHERE task_id = ?`, phase, updatedAt, taskID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE deployment_tasks SET status = ?, detail = ?, updated_at = ? WHERE id = ?`, status, detail, updatedAt, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) StageSiteDeletion(taskID string, updates []NodeStateUpdate, targets []PublishTaskNode) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, target := range targets {
		if target.NodeID == "" || target.TargetVersion < 1 {
			return errors.New("invalid site deletion node target")
		}
		if _, err := tx.Exec(`INSERT INTO publish_task_nodes(task_id, node_id, target_version, status) VALUES (?, ?, ?, ?)`,
			taskID, target.NodeID, target.TargetVersion, domain.PublishNodePending); err != nil {
			return err
		}
	}
	updatedAt := stamp(now())
	if err := saveNodeStatesTx(tx, updates, updatedAt); err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE site_deletion_jobs SET phase = ?, updated_at = ? WHERE task_id = ?`, SiteDeletionWaitingForEdges, updatedAt, taskID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE deployment_tasks SET status = ?, detail = ?, updated_at = ? WHERE id = ?`,
		domain.TaskApplying, "waiting for active edge nodes to remove the site", updatedAt, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SiteDeletionReady(taskID string) (bool, error) {
	task, err := s.GetTask(taskID)
	if err != nil {
		return false, err
	}
	if task.Status != domain.TaskQueued && task.Status != domain.TaskDispatching && task.Status != domain.TaskApplying {
		return false, nil
	}
	if _, err := s.db.Exec(`UPDATE publish_task_nodes
		SET status = ?, error_code = '', detail = 'edge confirmed applied version', port_conflicts_json = '[]', reported_at = ?
		WHERE task_id = ? AND status = ?
		AND EXISTS (SELECT 1 FROM nodes WHERE nodes.id = publish_task_nodes.node_id AND nodes.applied_version >= publish_task_nodes.target_version)`,
		domain.PublishNodeSucceeded, stamp(now()), taskID, domain.PublishNodePending); err != nil {
		return false, err
	}

	total, pending, succeeded, failed, err := s.deploymentTaskNodeCounts(taskID)
	if err != nil {
		return false, err
	}
	expired := task.DeadlineAt != nil && !task.DeadlineAt.After(now())
	if pending > 0 && !expired {
		return false, nil
	}
	if pending > 0 {
		if _, err := s.db.Exec(`UPDATE publish_task_nodes SET status = ?, error_code = 'confirmation_timeout',
			detail = 'edge did not confirm site removal before the deletion deadline' WHERE task_id = ? AND status = ?`,
			domain.PublishNodeTimedOut, taskID, domain.PublishNodePending); err != nil {
			return false, err
		}
		failed += pending
	}
	if failed > 0 {
		status := domain.TaskFailed
		detail := fmt.Sprintf("%d active edge node(s) did not remove the site", failed)
		if succeeded > 0 {
			status = domain.TaskPartial
			detail = fmt.Sprintf("site removed by %d of %d active edge node(s)", succeeded, total)
		}
		return false, s.UpdateTask(taskID, status, detail)
	}
	if total == 0 || succeeded == total {
		return true, s.SetSiteDeletionPhase(taskID, SiteDeletionFinalizing, domain.TaskApplying, "edge removal confirmed; cleaning certificate material")
	}
	return false, nil
}

func (s *Store) deploymentTaskNodeCounts(taskID string) (total, pending, succeeded, failed int, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status IN (?, ?) THEN 1 ELSE 0 END), 0)
		FROM publish_task_nodes WHERE task_id = ?`, domain.PublishNodePending, domain.PublishNodeSucceeded,
		domain.PublishNodeFailed, domain.PublishNodeTimedOut, taskID).Scan(&total, &pending, &succeeded, &failed)
	return
}

func (s *Store) FailSiteDeletion(taskID, detail string) error {
	return s.UpdateTask(taskID, domain.TaskFailed, detail)
}

func (s *Store) CompleteSiteDeletion(taskID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var siteID, siteName, actor, remoteAddr string
	if err := tx.QueryRow(`SELECT j.site_id, sites.name, j.actor, j.remote_addr
		FROM site_deletion_jobs j JOIN sites ON sites.id = j.site_id WHERE j.task_id = ?`, taskID).
		Scan(&siteID, &siteName, &actor, &remoteAddr); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	var confirmed int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM publish_task_nodes WHERE task_id = ? AND status = ?`, taskID, domain.PublishNodeSucceeded).Scan(&confirmed); err != nil {
		return err
	}
	completed := now()
	if _, err := tx.Exec(`DELETE FROM sites WHERE id = ?`, siteID); err != nil {
		return err
	}
	detail := fmt.Sprintf("site removed after confirmation from %d active edge node(s)", confirmed)
	if _, err := tx.Exec(`UPDATE deployment_tasks SET status = ?, detail = ?, updated_at = ? WHERE id = ?`,
		domain.TaskSucceeded, detail, stamp(completed), taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO audit_logs(id, actor, action, resource_type, resource_id, remote_addr, detail, created_at)
		VALUES (?, ?, 'delete', 'site', ?, ?, ?, ?)`, uuid.NewString(), actor, siteID, remoteAddr,
		"task="+taskID+" site="+siteName, stamp(completed)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LatestSiteDeletionTask(siteID string) (domain.DeploymentTask, error) {
	return scanTask(s.db.QueryRow(`SELECT id, kind, site_id, status, detail, deadline_at, created_at, updated_at
		FROM deployment_tasks WHERE site_id = ? AND kind = 'delete_site' ORDER BY created_at DESC LIMIT 1`, siteID))
}

func (s *Store) SiteDeletionStatus(siteID string) (domain.PublishStatus, error) {
	task, err := s.LatestSiteDeletionTask(siteID)
	if errors.Is(err, ErrNotFound) {
		return domain.PublishStatus{}, nil
	}
	if err != nil {
		return domain.PublishStatus{}, err
	}
	return s.deploymentStatus(task)
}

func (s *Store) deploymentStatus(task domain.DeploymentTask) (domain.PublishStatus, error) {
	rows, err := s.db.Query(`SELECT publish_task_nodes.node_id, nodes.name, publish_task_nodes.target_version,
		publish_task_nodes.status, publish_task_nodes.error_code, publish_task_nodes.detail,
		publish_task_nodes.port_conflicts_json, publish_task_nodes.reported_at
		FROM publish_task_nodes JOIN nodes ON nodes.id = publish_task_nodes.node_id
		WHERE publish_task_nodes.task_id = ? ORDER BY nodes.name`, task.ID)
	if err != nil {
		return domain.PublishStatus{}, err
	}
	defer rows.Close()
	result := domain.PublishStatus{Task: &task, Nodes: make([]domain.PublishNodeResult, 0)}
	for rows.Next() {
		var node domain.PublishNodeResult
		var conflicts string
		var reportedAt sql.NullString
		if err := rows.Scan(&node.NodeID, &node.NodeName, &node.TargetVersion, &node.Status, &node.ErrorCode,
			&node.Detail, &conflicts, &reportedAt); err != nil {
			return domain.PublishStatus{}, err
		}
		if err := json.Unmarshal([]byte(conflicts), &node.PortConflicts); err != nil {
			return domain.PublishStatus{}, fmt.Errorf("decode deployment port conflicts: %w", err)
		}
		if reportedAt.Valid {
			value, err := parseTime(reportedAt.String)
			if err != nil {
				return domain.PublishStatus{}, err
			}
			node.ReportedAt = &value
		}
		result.Nodes = append(result.Nodes, node)
	}
	return result, rows.Err()
}
