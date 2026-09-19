package protocol

import "time"

type CreateConnectionRequest struct {
	Name          string            `json:"name" msgpack:"name"`
	Kind          DatabaseKind      `json:"kind" msgpack:"kind"`
	Host          string            `json:"host,omitempty" msgpack:"host,omitempty"`
	Port          int               `json:"port,omitempty" msgpack:"port,omitempty"`
	Database      string            `json:"database,omitempty" msgpack:"database,omitempty"`
	Username      string            `json:"username,omitempty" msgpack:"username,omitempty"`
	PasswordRef   string            `json:"password_ref,omitempty" msgpack:"password_ref,omitempty"`
	Password      string            `json:"password,omitempty" msgpack:"password,omitempty"`
	Options       map[string]string `json:"options,omitempty" msgpack:"options,omitempty"`
	SSHTunnel     *SshTunnelConfig  `json:"ssh_tunnel,omitempty" msgpack:"ssh_tunnel,omitempty"`
	DefaultSchema string            `json:"default_schema,omitempty" msgpack:"default_schema,omitempty"`
	// ReadOnly 为 true 时 app-core 拒绝写语句与 KV 写（behavior.md §12.3）。
	// 用指针保证缺省即 false 且线上不编码（与 Rust Option<bool> 对齐）。
	ReadOnly *bool `json:"read_only,omitempty" msgpack:"read_only,omitempty"`
}

type UpdateConnectionRequest struct {
	Name          *string           `json:"name,omitempty" msgpack:"name,omitempty"`
	Host          *string           `json:"host,omitempty" msgpack:"host,omitempty"`
	Port          *int              `json:"port,omitempty" msgpack:"port,omitempty"`
	Database      *string           `json:"database,omitempty" msgpack:"database,omitempty"`
	Username      *string           `json:"username,omitempty" msgpack:"username,omitempty"`
	PasswordRef   *string           `json:"password_ref,omitempty" msgpack:"password_ref,omitempty"`
	Password      string            `json:"password,omitempty" msgpack:"password,omitempty"`
	Options       map[string]string `json:"options,omitempty" msgpack:"options,omitempty"`
	SSHTunnel     *SshTunnelConfig  `json:"ssh_tunnel,omitempty" msgpack:"ssh_tunnel,omitempty"`
	DefaultSchema *string           `json:"default_schema,omitempty" msgpack:"default_schema,omitempty"`
	ReadOnly      *bool             `json:"read_only,omitempty" msgpack:"read_only,omitempty"`
}

type ConnectionInfo struct {
	ID            string            `json:"id" msgpack:"id"`
	Name          string            `json:"name" msgpack:"name"`
	Kind          DatabaseKind      `json:"kind" msgpack:"kind"`
	Host          string            `json:"host,omitempty" msgpack:"host,omitempty"`
	Port          int               `json:"port,omitempty" msgpack:"port,omitempty"`
	Database      string            `json:"database,omitempty" msgpack:"database,omitempty"`
	Username      string            `json:"username,omitempty" msgpack:"username,omitempty"`
	Options       map[string]string `json:"options,omitempty" msgpack:"options,omitempty"`
	SSHTunnel     *SshTunnelConfig  `json:"ssh_tunnel,omitempty" msgpack:"ssh_tunnel,omitempty"`
	DefaultSchema string            `json:"default_schema,omitempty" msgpack:"default_schema,omitempty"`
	ReadOnly      *bool             `json:"read_only,omitempty" msgpack:"read_only,omitempty"`
	CreatedAt     time.Time         `json:"created_at" msgpack:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at" msgpack:"updated_at"`
}

type ConnectionStatus struct {
	ID            string  `json:"id" msgpack:"id"`
	Connected     bool    `json:"connected" msgpack:"connected"`
	ServerVersion string  `json:"server_version,omitempty" msgpack:"server_version,omitempty"`
	LatencyMs     float64 `json:"latency_ms,omitempty" msgpack:"latency_ms,omitempty"`
	Error         string  `json:"error,omitempty" msgpack:"error,omitempty"`
}
