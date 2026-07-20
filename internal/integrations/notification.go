package integrations

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type NotificationCategory string

const (
	NotificationCategoryAvailability NotificationCategory = "availability"
	NotificationCategoryMonitoring   NotificationCategory = "monitoring"
	NotificationCategoryCertificate  NotificationCategory = "certificate"
	NotificationCategoryBackup       NotificationCategory = "backup"
)

var notificationCategories = []NotificationCategory{
	NotificationCategoryAvailability,
	NotificationCategoryMonitoring,
	NotificationCategoryCertificate,
	NotificationCategoryBackup,
}

func NotificationCategories() []NotificationCategory {
	return append([]NotificationCategory(nil), notificationCategories...)
}

func ValidNotificationCategory(category NotificationCategory) bool {
	for _, candidate := range notificationCategories {
		if category == candidate {
			return true
		}
	}
	return false
}

type NotificationSeverity string

const (
	NotificationSeverityInfo    NotificationSeverity = "info"
	NotificationSeveritySuccess NotificationSeverity = "success"
	NotificationSeverityWarning NotificationSeverity = "warning"
	NotificationSeverityError   NotificationSeverity = "error"
)

type NotificationDetail struct {
	Label string
	Value string
}

type Notification struct {
	Category              NotificationCategory
	Severity              NotificationSeverity
	Subject               string
	Message               string
	Details               []NotificationDetail
	OccurredAt            time.Time
	Key                   string
	Cooldown              time.Duration
	SuppressUntilResolved bool
	Resolved              bool
	NotifyOnResolve       bool
}

type RichNotifier interface {
	NotifyNotification(context.Context, Notification) error
}

func SendNotification(ctx context.Context, notifier Notifier, notification Notification) error {
	if notifier == nil {
		return nil
	}
	if rich, ok := notifier.(RichNotifier); ok {
		return rich.NotifyNotification(ctx, notification)
	}
	if notification.Resolved && !notification.NotifyOnResolve {
		return nil
	}
	return notifier.Notify(ctx, notification.Subject, notification.PlainText())
}

func (notification Notification) PlainText() string {
	parts := make([]string, 0, len(notification.Details)+3)
	if message := strings.TrimSpace(notification.Message); message != "" {
		parts = append(parts, message)
	}
	for _, detail := range notification.Details {
		label := strings.TrimSpace(detail.Label)
		value := strings.TrimSpace(detail.Value)
		if label == "" || value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", label, value))
	}
	if !notification.OccurredAt.IsZero() {
		parts = append(parts, "Time: "+formatNotificationTime(notification.OccurredAt))
	}
	return strings.Join(parts, "\n")
}

func normalizeNotification(notification Notification) Notification {
	notification.Subject = strings.TrimSpace(notification.Subject)
	notification.Message = strings.TrimSpace(notification.Message)
	notification.Key = strings.TrimSpace(notification.Key)
	if !ValidNotificationCategory(notification.Category) {
		notification.Category = NotificationCategoryAvailability
	}
	switch notification.Severity {
	case NotificationSeverityInfo, NotificationSeveritySuccess, NotificationSeverityWarning, NotificationSeverityError:
	default:
		notification.Severity = NotificationSeverityInfo
	}
	if notification.OccurredAt.IsZero() {
		notification.OccurredAt = time.Now().UTC()
	} else {
		notification.OccurredAt = notification.OccurredAt.UTC()
	}
	return notification
}

func formatNotificationTime(value time.Time) string {
	beijing := time.FixedZone("CST", 8*60*60)
	return value.In(beijing).Format("2006-01-02 15:04:05 MST")
}
