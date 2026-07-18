package domain

import "time"

const (
	MessageInfo    = "info"
	MessageSuccess = "success"
	MessageWarning = "warning"
	MessageError   = "error"
)

type Message struct {
	ID           string     `json:"id"`
	Severity     string     `json:"severity"`
	Category     string     `json:"category"`
	Title        string     `json:"title"`
	Body         string     `json:"body,omitempty"`
	SourceType   string     `json:"source_type,omitempty"`
	SourceID     string     `json:"source_id,omitempty"`
	SourceStatus string     `json:"source_status,omitempty"`
	ResourceType string     `json:"resource_type,omitempty"`
	ResourceID   string     `json:"resource_id,omitempty"`
	ReadAt       *time.Time `json:"read_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}
