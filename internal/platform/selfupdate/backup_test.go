package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// writeFakeExe 在 dir 写一个「假可执行文件」并返回其路径（测试辅助）。
func writeFakeExe(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBackupCurrent_And_BackupInfo 备份当前二进制后能查到正确的版本/sha256。
func TestBackupCurrent_And_BackupInfo(t *testing.T) {
	root, err := dataroot.Init(t.TempDir())
	if err != nil {
		t.Fatalf("初始化数据根失败: %v", err)
	}
	exe := writeFakeExe(t, t.TempDir(), "app", "CURRENT-V1")

	// 无备份时 BackupInfo 应返回 ok=false。
	if _, ok := BackupInfo(ComponentControlPlane, root); ok {
		t.Fatal("尚未备份，BackupInfo 应返回 ok=false")
	}

	if err := BackupCurrentFrom(ComponentControlPlane, "0.1.0", exe, root); err != nil {
		t.Fatalf("BackupCurrentFrom 失败: %v", err)
	}

	meta, ok := BackupInfo(ComponentControlPlane, root)
	if !ok {
		t.Fatal("备份后 BackupInfo 应返回 ok=true")
	}
	if meta.Version != "0.1.0" {
		t.Fatalf("备份版本应为 0.1.0，实得 %q", meta.Version)
	}
	if meta.SHA256 != sha256Hex([]byte("CURRENT-V1")) {
		t.Fatalf("备份 sha256 不符: 实得 %q", meta.SHA256)
	}
	if meta.BackedUpAt.IsZero() {
		t.Fatal("备份时间应被设置")
	}
}

// TestBackupCurrent_OverwritesPrevious 再次备份覆盖上一份（只留一份）。
func TestBackupCurrent_OverwritesPrevious(t *testing.T) {
	root, err := dataroot.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe1 := writeFakeExe(t, dir, "app1", "V1-CONTENT")
	exe2 := writeFakeExe(t, dir, "app2", "V2-CONTENT")

	if err := BackupCurrentFrom(ComponentWorker, "1.0.0", exe1, root); err != nil {
		t.Fatal(err)
	}
	if err := BackupCurrentFrom(ComponentWorker, "2.0.0", exe2, root); err != nil {
		t.Fatal(err)
	}
	meta, ok := BackupInfo(ComponentWorker, root)
	if !ok || meta.Version != "2.0.0" {
		t.Fatalf("二次备份应覆盖为 2.0.0，实得 ok=%v version=%q", ok, meta.Version)
	}
	if meta.SHA256 != sha256Hex([]byte("V2-CONTENT")) {
		t.Fatalf("备份内容应为第二份，sha 不符")
	}
}

// TestRollback_RestoresBackup 回滚把目标可执行文件换回备份内容，并回报备份版本。
func TestRollback_RestoresBackup(t *testing.T) {
	root, err := dataroot.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// 旧二进制（将被备份），随后目标被「升级」为新内容，再回滚应换回旧内容。
	target := writeFakeExe(t, dir, "app", "OLD-VERSION")
	if err := BackupCurrentFrom(ComponentControlPlane, "0.9.0", target, root); err != nil {
		t.Fatal(err)
	}
	// 模拟升级：把 target 改成新内容。
	if err := os.WriteFile(target, []byte("NEW-VERSION-BROKEN"), 0o755); err != nil {
		t.Fatal(err)
	}

	meta, err := RollbackTo(ComponentControlPlane, target, root)
	if err != nil {
		t.Fatalf("RollbackTo 失败: %v", err)
	}
	if meta.Version != "0.9.0" {
		t.Fatalf("回滚应回报备份版本 0.9.0，实得 %q", meta.Version)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "OLD-VERSION" {
		t.Fatalf("回滚后目标内容应为备份的旧版本，实得 %q", got)
	}
	// 备份应原地保留（支持重复回滚）。
	if _, ok := BackupInfo(ComponentControlPlane, root); !ok {
		t.Fatal("回滚后备份应原地保留")
	}
}

// TestRollback_NoBackup 无备份时回滚返回 ErrNoBackup。
func TestRollback_NoBackup(t *testing.T) {
	root, err := dataroot.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := writeFakeExe(t, t.TempDir(), "app", "X")
	_, err = RollbackTo(ComponentWorker, target, root)
	if !errors.Is(err, ErrNoBackup) {
		t.Fatalf("无备份应返回 ErrNoBackup，实得 %v", err)
	}
}

// TestRollback_CorruptedBackup 备份二进制被损坏（sha 与 meta 不符）时拒绝回滚、不替换。
func TestRollback_CorruptedBackup(t *testing.T) {
	root, err := dataroot.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := writeFakeExe(t, dir, "app", "GOOD-OLD")
	if err := BackupCurrentFrom(ComponentControlPlane, "0.9.0", target, root); err != nil {
		t.Fatal(err)
	}
	// 篡改备份二进制（使其 sha 与 meta.json 记录的不符）。
	backupBin := filepath.Join(backupDir(ComponentControlPlane, root), backupBinaryName)
	if err := os.WriteFile(backupBin, []byte("TAMPERED"), 0o755); err != nil {
		t.Fatal(err)
	}
	// target 被升级为新内容。
	if err := os.WriteFile(target, []byte("CURRENT-NEW"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err = RollbackTo(ComponentControlPlane, target, root)
	if err == nil {
		t.Fatal("备份损坏应拒绝回滚")
	}
	if errors.Is(err, ErrNoBackup) {
		t.Fatal("备份存在但损坏，不应是 ErrNoBackup")
	}
	// 目标绝不能被换成损坏的备份。
	got, _ := os.ReadFile(target)
	if string(got) != "CURRENT-NEW" {
		t.Fatalf("备份损坏时目标不应被改动，实得 %q", got)
	}
}
