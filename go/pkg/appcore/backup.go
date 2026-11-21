package appcore

// 库级备份/恢复（§387）：SQLite 用 VACUUM INTO 做一致性快照（不经过
// 结果集导出——导出是数据面，备份是库文件面）。恢复是把备份文件登记
// 为一个新连接（不覆盖现有库——破坏性动作由用户显式选目标）。
//
// 其它引擎（MySQL/PG/MSSQL/Oracle）的原生 dump 工具需要外部二进制，
// polydb 的本地优先定位暂只覆盖 SQLite——诚实报错而非伪造。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/polydb/polydb/pkg/protocol"
)

// BackupDatabase 把 SQLite 连接的库文件一致性快照写到 outPath。
// VACUUM INTO 的目标必须不存在（SQLite 的硬约束）——存在时先拒绝。
func (a *AppCore) BackupDatabase(ctx context.Context, id, outPath string) (int64, error) {
	info, err := a.GetConnection(id)
	if err != nil {
		return 0, err
	}
	if !strings.EqualFold(string(info.Kind), "sqlite") {
		return 0, fmt.Errorf("备份目前只支持 SQLite（引擎 %s 未提供通用 dump 面）", info.Kind)
	}
	if strings.TrimSpace(outPath) == "" {
		return 0, fmt.Errorf("缺少输出路径")
	}
	if _, err := os.Stat(outPath); err == nil {
		return 0, fmt.Errorf("目标已存在（VACUUM INTO 拒绝覆盖）: %s", outPath)
	} else if !os.IsNotExist(err) {
		return 0, err
	}

	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return 0, err
	}
	// VACUUM INTO 用字面量路径——先按 SQLite 的引号规则包好（路径中的
	// 单引号翻倍）。目标路径来自用户选择的对话框，不是 SQL 语句拼装。
	quoted := "'" + strings.ReplaceAll(outPath, "'", "''") + "'"
	if _, err := d.Execute(ctx, "VACUUM INTO "+quoted); err != nil {
		return 0, fmt.Errorf("VACUUM INTO: %w", err)
	}
	st, err := os.Stat(outPath)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// RestoreDatabase 把一个 SQLite 备份文件登记为新的本地连接（source 是
// 文件路径——不触碰现有连接）。返回新连接 id。
func (a *AppCore) RestoreDatabase(ctx context.Context, file, name string) (string, error) {
	if strings.TrimSpace(file) == "" {
		return "", fmt.Errorf("缺少备份文件路径")
	}
	if _, err := os.Stat(file); err != nil {
		return "", fmt.Errorf("备份文件不可读: %w", err)
	}
	if strings.TrimSpace(name) == "" {
		name = "restored-" + time.Now().UTC().Format("20060102-150405")
	}
	conn, err := a.CreateConnection(&protocol.CreateConnectionRequest{
		Name: name, Kind: protocol.DatabaseKindSQLite, Database: file,
	})
	if err != nil {
		return "", err
	}
	return conn.ID, nil
}
