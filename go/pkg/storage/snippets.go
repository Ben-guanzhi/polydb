package storage

// SQL 片段收藏的持久化（§387）：TUI 侧对等 web 的 customTemplates
// （localStorage）——本地 meta 库加一张 snippets 表，CRUD 走同一 *sql.DB。

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Snippet 是一条用户收藏的 SQL 片段。
type Snippet struct {
	ID        string
	Name      string
	SQL       string
	Group     string
	CreatedAt string
	UpdatedAt string
}

// initSnippetsSchema 在 initSchema 里调用（老库平滑迁移：IF NOT EXISTS）。
func initSnippetsSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS snippets (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		sql        TEXT NOT NULL,
		"group"    TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`)
	if err != nil {
		return fmt.Errorf("init snippets: %w", err)
	}
	return nil
}

// SnippetRepository 是片段收藏的仓储（与 ConnectionRepository 同构）。
type SnippetRepository struct{ db *sql.DB }

func NewSnippetRepository(db *sql.DB) *SnippetRepository { return &SnippetRepository{db: db} }

// SaveSnippet 插入或更新（按 id）。
func (r *SnippetRepository) SaveSnippet(ctx context.Context, sn Snippet) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx, `INSERT INTO snippets (id, name, sql, "group", created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, sql=excluded.sql,
			"group"=excluded."group", updated_at=excluded.updated_at`,
		sn.ID, sn.Name, sn.SQL, sn.Group, now, now)
	return err
}

// ListSnippets 按 group/name 排序列出。
func (r *SnippetRepository) ListSnippets(ctx context.Context) ([]Snippet, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, sql, "group", created_at, updated_at
		FROM snippets ORDER BY "group", name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snippet
	for rows.Next() {
		var x Snippet
		if err := rows.Scan(&x.ID, &x.Name, &x.SQL, &x.Group, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// DeleteSnippet 按 id 删除。
func (r *SnippetRepository) DeleteSnippet(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM snippets WHERE id = ?`, id)
	return err
}
