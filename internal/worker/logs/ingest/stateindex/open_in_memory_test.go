package stateindex

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenInMemorySucceeds 回归（2026-10-30）：
//
// OpenInMemory 曾用 execPragma（内部 QueryRowContext+Scan）去设赋值型 pragma，
// 而 `PRAGMA foreign_keys=ON` / `PRAGMA cache_size=..` **不返回结果集** ⇒ Scan 稳定得到
// sql.ErrNoRows ⇒ OpenInMemory 永远失败 ⇒ ingest 拒绝启动采集（整条日志链路倒下）。
//
// 这个错误在 P1（7456d716）落地时没被发现，因为当时只跑了 build + vet。本用例钉住
// 「内存库能打开、pragma 真的生效」。变异（把 ExecContext 改回 execPragma）⇒ 本条转红。
func TestOpenInMemorySucceeds(t *testing.T) {
	// path 只作标识，不建文件。
	path := filepath.Join(t.TempDir(), "unused.index.db")
	s, err := OpenInMemory(path, DefaultCommitBudget())
	require.NoError(t, err, "纯内存索引库必须能打开（赋值型 pragma 不得走 QueryRow+Scan）")
	t.Cleanup(func() { _ = s.Close() })

	var fk int
	require.NoError(t, s.db.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&fk))
	require.Equal(t, 1, fk, "foreign_keys 必须真的开启")
}

// TestOpenInMemoryCreatesNoFile path 只是标识，内存库不得在磁盘上留下任何文件。
func TestOpenInMemoryCreatesNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "must-not-exist.index.db")
	s, err := OpenInMemory(path, DefaultCommitBudget())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoFileExists(t, path, "用户裁定：机械盘不做任何落盘")
}
