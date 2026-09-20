package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/protocol"
)

// ConnectionRepository 提供 connections 表的 CRUD，密码只落 password_ref 不落明文。
type ConnectionRepository struct {
	db *sql.DB
}

const connColumns = "id, name, kind, host, port, database, username, options, ssh_tunnel, default_schema, read_only, \"group\", color, created_at, updated_at"

func NewConnectionRepository(db *sql.DB) *ConnectionRepository {
	return &ConnectionRepository{db: db}
}

func (r *ConnectionRepository) Create(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
	now := time.Now().UTC()
	id := uuid.NewString()
	optionsJSON, err := json.Marshal(req.Options)
	if err != nil {
		return protocol.ConnectionInfo{}, fmt.Errorf("marshal options: %w", err)
	}
	var sshJSON []byte
	if req.SSHTunnel != nil {
		sshJSON, err = json.Marshal(req.SSHTunnel)
		if err != nil {
			return protocol.ConnectionInfo{}, fmt.Errorf("marshal ssh: %w", err)
		}
	}
	_, err = r.db.Exec(
		`INSERT INTO connections (id, name, kind, host, port, database, username, password_ref, options, ssh_tunnel, default_schema, read_only, "group", color, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, req.Name, string(req.Kind), nullStr(req.Host), nullInt(req.Port),
		nullStr(req.Database), nullStr(req.Username), nullStr(req.PasswordRef),
		string(optionsJSON), nullBytes(sshJSON), nullStr(req.DefaultSchema), boolInt(req.ReadOnly),
		nullStr(derefStr(req.Group)), nullStr(derefStr(req.Color)),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return protocol.ConnectionInfo{}, fmt.Errorf("insert connection: %w", err)
	}
	return protocol.ConnectionInfo{
		ID:            id,
		Name:          req.Name,
		Kind:          req.Kind,
		Host:          req.Host,
		Port:          req.Port,
		Database:      req.Database,
		Username:      req.Username,
		Options:       req.Options,
		SSHTunnel:     req.SSHTunnel,
		DefaultSchema: req.DefaultSchema,
		ReadOnly:      req.ReadOnly,
		Group:         req.Group,
		Color:         req.Color,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (r *ConnectionRepository) List() ([]protocol.ConnectionInfo, error) {
	rows, err := r.db.Query("SELECT " + connColumns + " FROM connections ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	out := make([]protocol.ConnectionInfo, 0)
	for rows.Next() {
		info, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

func (r *ConnectionRepository) Get(id string) (protocol.ConnectionInfo, error) {
	row := r.db.QueryRow("SELECT "+connColumns+" FROM connections WHERE id = ?", id)
	info, err := scanConnection(row)
	if err == sql.ErrNoRows {
		return protocol.ConnectionInfo{}, ErrNotFound
	}
	return info, err
}

func (r *ConnectionRepository) Update(id string, req *protocol.UpdateConnectionRequest) (protocol.ConnectionInfo, error) {
	existing, err := r.Get(id)
	if err != nil {
		return protocol.ConnectionInfo{}, err
	}
	now := time.Now().UTC()

	var passwordRef string
	if req.PasswordRef != nil {
		passwordRef = *req.PasswordRef
	} else {
		var ref sql.NullString
		if err := r.db.QueryRow("SELECT password_ref FROM connections WHERE id = ?", id).Scan(&ref); err != nil && err != sql.ErrNoRows {
			return protocol.ConnectionInfo{}, fmt.Errorf("read password_ref: %w", err)
		}
		passwordRef = ref.String
	}

	name := existing.Name
	host := existing.Host
	port := existing.Port
	database := existing.Database
	username := existing.Username
	options := existing.Options
	ssh := existing.SSHTunnel
	defSchema := existing.DefaultSchema
	readOnly := existing.ReadOnly
	group := existing.Group
	color := existing.Color

	if req.Name != nil {
		name = *req.Name
	}
	if req.Host != nil {
		host = *req.Host
	}
	if req.Port != nil {
		port = *req.Port
	}
	if req.Database != nil {
		database = *req.Database
	}
	if req.Username != nil {
		username = *req.Username
	}
	if req.Options != nil {
		options = req.Options
	}
	if req.SSHTunnel != nil {
		ssh = req.SSHTunnel
	}
	if req.DefaultSchema != nil {
		defSchema = *req.DefaultSchema
	}
	if req.ReadOnly != nil {
		readOnly = req.ReadOnly
	}
	if req.Group != nil {
		group = req.Group
	}
	if req.Color != nil {
		color = req.Color
	}

	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return protocol.ConnectionInfo{}, fmt.Errorf("marshal options: %w", err)
	}
	var sshJSON []byte
	if ssh != nil {
		sshJSON, err = json.Marshal(ssh)
		if err != nil {
			return protocol.ConnectionInfo{}, fmt.Errorf("marshal ssh: %w", err)
		}
	}
	_, err = r.db.Exec(
		`UPDATE connections SET name=?, host=?, port=?, database=?, username=?, password_ref=?, options=?, ssh_tunnel=?, default_schema=?, read_only=?, "group"=?, color=?, updated_at=? WHERE id=?`,
		name, nullStr(host), nullInt(port), nullStr(database), nullStr(username),
		nullStr(passwordRef), string(optionsJSON), nullBytes(sshJSON), nullStr(defSchema), boolInt(readOnly),
		nullStr(derefStr(group)), nullStr(derefStr(color)),
		now.Format(time.RFC3339), id,
	)
	if err != nil {
		return protocol.ConnectionInfo{}, fmt.Errorf("update connection: %w", err)
	}
	return protocol.ConnectionInfo{
		ID:            id,
		Name:          name,
		Kind:          existing.Kind,
		Host:          host,
		Port:          port,
		Database:      database,
		Username:      username,
		Options:       options,
		SSHTunnel:     ssh,
		DefaultSchema: defSchema,
		ReadOnly:      readOnly,
		Group:         group,
		Color:         color,
		CreatedAt:     existing.CreatedAt,
		UpdatedAt:     now,
	}, nil
}

func (r *ConnectionRepository) Delete(id string) (bool, error) {
	res, err := r.db.Exec("DELETE FROM connections WHERE id = ?", id)
	if err != nil {
		return false, fmt.Errorf("delete connection: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetPasswordRef 返回连接存储的 password_ref；该引用不随 ConnectionInfo 下发（红线）。
func (r *ConnectionRepository) GetPasswordRef(id string) (string, error) {
	var ref sql.NullString
	err := r.db.QueryRow("SELECT password_ref FROM connections WHERE id = ?", id).Scan(&ref)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read password_ref: %w", err)
	}
	return ref.String, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanConnection(s rowScanner) (protocol.ConnectionInfo, error) {
	var (
		id, name, kind                string
		host, database, username      sql.NullString
		port                          sql.NullInt64
		optionsJSON, created, updated string
		sshJSON                       sql.NullString
		defSchema                     sql.NullString
		readOnly                      int
		group, color                  sql.NullString
	)
	err := s.Scan(&id, &name, &kind, &host, &port, &database, &username,
		&optionsJSON, &sshJSON, &defSchema, &readOnly, &group, &color, &created, &updated)
	if err != nil {
		return protocol.ConnectionInfo{}, err
	}
	var options map[string]string
	if err := json.Unmarshal([]byte(optionsJSON), &options); err != nil {
		options = map[string]string{}
	}
	var ssh *protocol.SshTunnelConfig
	if sshJSON.Valid && sshJSON.String != "" {
		if err := json.Unmarshal([]byte(sshJSON.String), &ssh); err != nil {
			return protocol.ConnectionInfo{}, fmt.Errorf("parse ssh: %w", err)
		}
	}
	createdAt, _ := time.Parse(time.RFC3339, created)
	updatedAt, _ := time.Parse(time.RFC3339, updated)
	var ro *bool
	if readOnly != 0 {
		ro = &readOnlyFlag
	}
	return protocol.ConnectionInfo{
		ID:            id,
		Name:          name,
		Kind:          protocol.DatabaseKind(kind),
		Host:          host.String,
		Port:          int(port.Int64),
		Database:      database.String,
		Username:      username.String,
		Options:       options,
		SSHTunnel:     ssh,
		DefaultSchema: defSchema.String,
		ReadOnly:      ro,
		Group:         nullStrPtr(group),
		Color:         nullStrPtr(color),
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// boolInt 把可选布尔写成 0/1（nil 视为 false），供 read_only 列使用。
func boolInt(v *bool) int {
	if v != nil && *v {
		return 1
	}
	return 0
}

func derefStr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func nullStrPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

var readOnlyFlag = true
