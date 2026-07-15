package domain

import "time"

type NodeStatus string

const (
	NodePending      NodeStatus = "pending"
	NodeActive       NodeStatus = "active"
	NodeDraining     NodeStatus = "draining"
	NodeRevoked      NodeStatus = "revoked"
	NodeUninstalling NodeStatus = "uninstalling"
	NodeUninstalled  NodeStatus = "uninstalled"
)

type TaskStatus string

const (
	TaskQueued      TaskStatus = "queued"
	TaskDispatching TaskStatus = "dispatching"
	TaskApplying    TaskStatus = "applying"
	TaskSucceeded   TaskStatus = "succeeded"
	TaskPartial     TaskStatus = "partial"
	TaskFailed      TaskStatus = "failed"
	TaskRolledBack  TaskStatus = "rolled_back"
)

type Node struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	PublicIPv4      string     `json:"public_ipv4"`
	Status          NodeStatus `json:"status"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	AppliedVersion  int64      `json:"applied_version"`
	LastError       string     `json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type Origin struct {
	URL        string `json:"url"`
	HostHeader string `json:"host_header"`
	Enabled    bool   `json:"enabled"`
}

const (
	DefaultClientMaxBodySizeMB     = 128
	MaxClientMaxBodySizeMB         = 1024
	DefaultReadWriteTimeoutSeconds = 360
)

type Site struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ZoneID        string   `json:"zone_id"`
	Domains       []string `json:"domains"`
	Nodes         []string `json:"node_ids"`
	PrimaryOrigin Origin   `json:"primary_origin"`
	BackupOrigin  *Origin  `json:"backup_origin,omitempty"`
	// StreamPaths is retained as an empty compatibility field for older API clients.
	StreamPaths             []string  `json:"stream_paths"`
	Passthrough             bool      `json:"passthrough"`
	ClientMaxBodySizeMB     int       `json:"client_max_body_size_mb"`
	ReadWriteTimeoutSeconds int       `json:"read_write_timeout_seconds"`
	CacheGeneration         int64     `json:"cache_generation"`
	ConfigVersion           int64     `json:"config_version"`
	Published               bool      `json:"published"`
	Enabled                 bool      `json:"enabled"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type EnrollmentToken struct {
	Token     string    `json:"token"`
	NodeID    string    `json:"node_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type DeploymentTask struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	SiteID     string     `json:"site_id,omitempty"`
	Status     TaskStatus `json:"status"`
	Detail     string     `json:"detail,omitempty"`
	DeadlineAt *time.Time `json:"deadline_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type ApplyStatus string

const (
	ApplySucceeded ApplyStatus = "succeeded"
	ApplyFailed    ApplyStatus = "failed"
)

// PortConflict identifies a local TCP listener that prevents edge Nginx from
// owning one of its public ports. It is reported to the authenticated control
// plane; the agent never terminates the conflicting process.
type PortConflict struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process"`
}

// ApplyReport is sent with an edge heartbeat after an attempt to apply a
// desired configuration. Older agents omit it and remain protocol-compatible.
type ApplyReport struct {
	Version       int64          `json:"version"`
	Status        ApplyStatus    `json:"status"`
	Code          string         `json:"code,omitempty"`
	Detail        string         `json:"detail,omitempty"`
	PortConflicts []PortConflict `json:"port_conflicts,omitempty"`
}

type PublishNodeStatus string

const (
	PublishNodePending   PublishNodeStatus = "pending"
	PublishNodeSucceeded PublishNodeStatus = "succeeded"
	PublishNodeFailed    PublishNodeStatus = "failed"
	PublishNodeTimedOut  PublishNodeStatus = "timed_out"
)

type PublishNodeResult struct {
	NodeID        string            `json:"node_id"`
	NodeName      string            `json:"node_name"`
	TargetVersion int64             `json:"target_version"`
	Status        PublishNodeStatus `json:"status"`
	ErrorCode     string            `json:"error_code,omitempty"`
	Detail        string            `json:"detail,omitempty"`
	PortConflicts []PortConflict    `json:"port_conflicts,omitempty"`
	ReportedAt    *time.Time        `json:"reported_at,omitempty"`
}

type PublishStatus struct {
	Task  *DeploymentTask     `json:"task"`
	Nodes []PublishNodeResult `json:"nodes"`
}

type DesiredState struct {
	Version      int64                `json:"version"`
	NginxConfig  string               `json:"nginx_config"`
	PublicPorts  []int                `json:"public_ports,omitempty"`
	Certificates map[string]TLSBundle `json:"certificates,omitempty"`
}

type TLSBundle struct {
	CertificatePEM string `json:"certificate_pem"`
	PrivateKeyPEM  string `json:"private_key_pem"`
}

type AccessLogEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	NodeID      string    `json:"node_id"`
	SiteID      string    `json:"site_id"`
	ClientIP    string    `json:"client_ip"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Status      int       `json:"status"`
	Bytes       int64     `json:"bytes"`
	DurationMS  int64     `json:"duration_ms"`
	Upstream    string    `json:"upstream"`
	CacheStatus string    `json:"cache_status"`
}
