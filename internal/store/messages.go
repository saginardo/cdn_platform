package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"cdn-platform/internal/domain"
	"github.com/google/uuid"
)

type MessagePage struct {
	Messages    []domain.Message `json:"messages"`
	UnreadCount int              `json:"unread_count"`
}

func (s *Store) CreateMessageOnce(message domain.Message) (domain.Message, bool, error) {
	message.Severity = strings.TrimSpace(message.Severity)
	message.Category = strings.TrimSpace(message.Category)
	message.Title = strings.TrimSpace(message.Title)
	message.Body = strings.TrimSpace(message.Body)
	if message.ID == "" {
		message.ID = uuid.NewString()
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = now()
	}
	if !validMessageSeverity(message.Severity) || message.Category == "" || message.Title == "" {
		return domain.Message{}, false, errors.New("message severity, category, and title are required")
	}
	var sourceType, sourceID, sourceStatus any
	if message.SourceType != "" || message.SourceID != "" || message.SourceStatus != "" {
		if message.SourceType == "" || message.SourceID == "" || message.SourceStatus == "" {
			return domain.Message{}, false, errors.New("message source fields must be set together")
		}
		sourceType, sourceID, sourceStatus = message.SourceType, message.SourceID, message.SourceStatus
	}
	result, err := s.db.Exec(`INSERT OR IGNORE INTO messages(
		id, severity, category, title, body, source_type, source_id, source_status,
		resource_type, resource_id, read_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		message.ID, message.Severity, message.Category, message.Title, message.Body,
		sourceType, sourceID, sourceStatus, message.ResourceType, message.ResourceID, stamp(message.CreatedAt))
	if err != nil {
		return domain.Message{}, false, err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return domain.Message{}, false, err
	}
	if created != 0 {
		return message, true, nil
	}
	if sourceType == nil {
		return domain.Message{}, false, errors.New("message was not created")
	}
	existing, err := scanMessage(s.db.QueryRow(`SELECT id, severity, category, title, body,
		source_type, source_id, source_status, resource_type, resource_id, read_at, created_at
		FROM messages WHERE source_type = ? AND source_id = ? AND source_status = ?`, sourceType, sourceID, sourceStatus))
	return existing, false, err
}

func (s *Store) Messages(limit int, unreadOnly bool) (MessagePage, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, severity, category, title, body, source_type, source_id, source_status,
		resource_type, resource_id, read_at, created_at FROM messages WHERE dismissed_at IS NULL`
	if unreadOnly {
		query += ` AND read_at IS NULL`
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return MessagePage{}, err
	}
	defer rows.Close()
	page := MessagePage{Messages: make([]domain.Message, 0)}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return MessagePage{}, err
		}
		page.Messages = append(page.Messages, message)
	}
	if err := rows.Err(); err != nil {
		return MessagePage{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE dismissed_at IS NULL AND read_at IS NULL`).Scan(&page.UnreadCount); err != nil {
		return MessagePage{}, err
	}
	return page, nil
}

func (s *Store) MarkMessageRead(id string) error {
	result, err := s.db.Exec(`UPDATE messages SET read_at = COALESCE(read_at, ?) WHERE id = ? AND dismissed_at IS NULL`, stamp(now()), id)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkAllMessagesRead() error {
	_, err := s.db.Exec(`UPDATE messages SET read_at = ? WHERE dismissed_at IS NULL AND read_at IS NULL`, stamp(now()))
	return err
}

func (s *Store) DeleteMessage(id string) error {
	result, err := s.db.Exec(`UPDATE messages SET dismissed_at = ? WHERE id = ? AND dismissed_at IS NULL`, stamp(now()), id)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ReconcileTaskMessages() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := stamp(now().AddDate(0, -3, 0))
	candidates, err := deploymentTaskMessageCandidates(tx, cutoff)
	if err != nil {
		return err
	}
	upgrades, err := nodeUpgradeMessageCandidates(tx, cutoff)
	if err != nil {
		return err
	}
	uninstalls, err := nodeUninstallMessageCandidates(tx, cutoff)
	if err != nil {
		return err
	}
	candidates = append(candidates, upgrades...)
	candidates = append(candidates, uninstalls...)
	for _, message := range candidates {
		_, err := tx.Exec(`INSERT OR IGNORE INTO messages(
			id, severity, category, title, body, source_type, source_id, source_status,
			resource_type, resource_id, read_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
			uuid.NewString(), message.Severity, message.Category, message.Title, message.Body,
			message.SourceType, message.SourceID, message.SourceStatus,
			message.ResourceType, message.ResourceID, stamp(message.CreatedAt))
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM messages
		WHERE (read_at IS NOT NULL OR dismissed_at IS NOT NULL) AND created_at < ?`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

func deploymentTaskMessageCandidates(tx *sql.Tx, cutoff string) ([]domain.Message, error) {
	rows, err := tx.Query(`SELECT task.id, task.kind, COALESCE(task.site_id, ''), task.status, task.detail,
		COALESCE(site.name, ''), task.updated_at
		FROM deployment_tasks AS task
		LEFT JOIN sites AS site ON site.id = task.site_id
		WHERE task.status IN (?, ?, ?, ?) AND task.updated_at >= ? AND NOT EXISTS (
			SELECT 1 FROM messages WHERE source_type = 'deployment_task'
			AND source_id = task.id AND source_status = task.status)
		ORDER BY task.updated_at LIMIT 200`, domain.TaskQueued, domain.TaskSucceeded, domain.TaskPartial, domain.TaskFailed, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []domain.Message
	for rows.Next() {
		var id, kind, siteID, status, detail, siteName, updatedAt string
		if err := rows.Scan(&id, &kind, &siteID, &status, &detail, &siteName, &updatedAt); err != nil {
			return nil, err
		}
		createdAt, err := parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		label := deploymentTaskLabel(kind)
		messages = append(messages, domain.Message{
			Severity: messageSeverityForStatus(status), Category: "task",
			Title: label + messageStatusSuffix(status), Body: messageBody(siteName, detail),
			SourceType: "deployment_task", SourceID: id, SourceStatus: status,
			ResourceType: "site", ResourceID: siteID, CreatedAt: createdAt,
		})
	}
	return messages, rows.Err()
}

func nodeUpgradeMessageCandidates(tx *sql.Tx, cutoff string) ([]domain.Message, error) {
	rows, err := tx.Query(`SELECT task.id, task.node_id, task.status, task.detail,
		COALESCE(node.name, ''), task.updated_at
		FROM node_upgrade_tasks AS task JOIN nodes AS node ON node.id = task.node_id
		WHERE task.status IN (?, ?, ?) AND task.updated_at >= ? AND NOT EXISTS (
			SELECT 1 FROM messages WHERE source_type = 'node_upgrade'
			AND source_id = task.id AND source_status = task.status)
		ORDER BY task.updated_at LIMIT 200`, domain.NodeUpgradeQueued, domain.NodeUpgradeSucceeded, domain.NodeUpgradeFailed, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []domain.Message
	for rows.Next() {
		var id, nodeID, status, detail, nodeName, updatedAt string
		if err := rows.Scan(&id, &nodeID, &status, &detail, &nodeName, &updatedAt); err != nil {
			return nil, err
		}
		createdAt, err := parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		messages = append(messages, domain.Message{
			Severity: messageSeverityForStatus(status), Category: "task",
			Title: "节点升级" + messageStatusSuffix(status), Body: messageBody(nodeName, detail),
			SourceType: "node_upgrade", SourceID: id, SourceStatus: status,
			ResourceType: "node", ResourceID: nodeID, CreatedAt: createdAt,
		})
	}
	return messages, rows.Err()
}

func nodeUninstallMessageCandidates(tx *sql.Tx, cutoff string) ([]domain.Message, error) {
	rows, err := tx.Query(`SELECT job.node_id, job.status, job.detail, COALESCE(node.name, ''), job.created_at, job.updated_at
		FROM node_uninstall_jobs AS job JOIN nodes AS node ON node.id = job.node_id
		WHERE job.status IN (?, ?, ?, ?, ?) AND job.updated_at >= ? AND NOT EXISTS (
			SELECT 1 FROM messages WHERE source_type = 'node_uninstall'
			AND source_id = job.node_id || ':' || job.created_at AND source_status = job.status)
		ORDER BY job.updated_at LIMIT 200`, NodeUninstallPreparing, NodeUninstallFailed, NodeUninstallSucceeded, NodeUninstallForced, NodeUninstallCanceled, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []domain.Message
	for rows.Next() {
		var nodeID, status, detail, nodeName, lifecycleCreatedAt, updatedAt string
		if err := rows.Scan(&nodeID, &status, &detail, &nodeName, &lifecycleCreatedAt, &updatedAt); err != nil {
			return nil, err
		}
		createdAt, err := parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		messages = append(messages, domain.Message{
			Severity: messageSeverityForStatus(status), Category: "task",
			Title: "节点卸载" + messageStatusSuffix(status), Body: messageBody(nodeName, detail),
			SourceType: "node_uninstall", SourceID: nodeID + ":" + lifecycleCreatedAt, SourceStatus: status,
			ResourceType: "node", ResourceID: nodeID, CreatedAt: createdAt,
		})
	}
	return messages, rows.Err()
}

func scanMessage(scanner interface{ Scan(...any) error }) (domain.Message, error) {
	var message domain.Message
	var sourceType, sourceID, sourceStatus, readAt sql.NullString
	var createdAt string
	err := scanner.Scan(&message.ID, &message.Severity, &message.Category, &message.Title, &message.Body,
		&sourceType, &sourceID, &sourceStatus, &message.ResourceType, &message.ResourceID, &readAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, ErrNotFound
	}
	if err != nil {
		return domain.Message{}, err
	}
	message.SourceType, message.SourceID, message.SourceStatus = sourceType.String, sourceID.String, sourceStatus.String
	message.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return domain.Message{}, err
	}
	if readAt.Valid {
		parsed, err := parseTime(readAt.String)
		if err != nil {
			return domain.Message{}, err
		}
		message.ReadAt = &parsed
	}
	return message, nil
}

func validMessageSeverity(value string) bool {
	return value == domain.MessageInfo || value == domain.MessageSuccess || value == domain.MessageWarning || value == domain.MessageError
}

func deploymentTaskLabel(kind string) string {
	switch kind {
	case "publish_site":
		return "站点发布"
	case "delete_site":
		return "站点删除"
	case "issue_certificate":
		return "证书签发"
	case "renew_certificate":
		return "证书续期"
	case "invalidate_cache":
		return "缓存刷新"
	default:
		return "部署任务"
	}
}

func messageStatusSuffix(status string) string {
	switch status {
	case "queued", "preparing":
		return "已开始"
	case "succeeded":
		return "成功"
	case string(domain.TaskPartial):
		return "部分完成"
	case string(NodeUninstallForced):
		return "已强制完成"
	case string(NodeUninstallCanceled):
		return "已取消"
	default:
		return "失败"
	}
}

func messageSeverityForStatus(status string) string {
	switch status {
	case "succeeded":
		return domain.MessageSuccess
	case "failed":
		return domain.MessageError
	case string(domain.TaskPartial), string(NodeUninstallForced):
		return domain.MessageWarning
	default:
		return domain.MessageInfo
	}
}

func messageBody(resourceName, detail string) string {
	resourceName = strings.TrimSpace(resourceName)
	detail = strings.TrimSpace(detail)
	if resourceName == "" {
		return detail
	}
	if detail == "" {
		return resourceName
	}
	return fmt.Sprintf("%s: %s", resourceName, detail)
}
