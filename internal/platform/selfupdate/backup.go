package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// 自更新组件标识（备份目录 <component> 段取值；与 controlplane service 的 Component* 取值一致）。
// 定义在 platform 层避免反向依赖 controlplane（架构不变量：platform 不得依赖上层包）。
const (
	// ComponentControlPlane 是 Control Plane 组件标识。
	ComponentControlPlane = "control-plane"
	// ComponentWorker 是 Worker Node 组件标识。
	ComponentWorker = "worker"
)

// ErrNoBackup 无可回滚的备份（从未升级过 / 备份元数据缺失）。
var ErrNoBackup = errors.New("无可回滚的备份")

// backupRootDir 是备份在数据根 cache/ 下的子目录名（FR-182，见 ADR-042）。
const backupRootDir = "selfupdate-backup"

// backupBinaryName 是备份目录内备份可执行文件的固定文件名。
const backupBinaryName = "binary"

// backupMetaName 是备份目录内元数据文件名。
const backupMetaName = "meta.json"

// BackupMeta 描述一份升级前备份的元数据（落 meta.json）。
type BackupMeta struct {
	// Version 是备份时的（被备份二进制的）版本号——回滚后即回报为「回滚到的版本」。
	Version string `json:"version"`
	// SHA256 是备份二进制的十六进制 sha256（小写）；回滚前据此校验备份未被损坏。
	SHA256 string `json:"sha256"`
	// BackedUpAt 是备份发生的时间。
	BackedUpAt time.Time `json:"backedUpAt"`
}

// backupDir 返回组件备份目录 <root.CacheDir()>/selfupdate-backup/<component>/。
// root 为 nil（极少数未初始化场景）时回退系统临时目录，与升级下载落 cache 的兜底一致。
func backupDir(component string, root *dataroot.Root) string {
	if root != nil {
		return filepath.Join(root.CacheDir(), backupRootDir, component)
	}
	return filepath.Join(os.TempDir(), "jianmanager-"+backupRootDir, component)
}

// BackupCurrent 备份当前进程的可执行文件（os.Executable()）到组件备份目录（FR-182，见 ADR-042）。
// currentVersion 为被备份二进制的版本号（写入 meta，回滚时回报）。升级流程在替换前调用。
// 每组件只留一份，再次备份覆盖上一份。
func BackupCurrent(component, currentVersion string, root *dataroot.Root) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	return BackupCurrentFrom(component, currentVersion, exe, root)
}

// BackupCurrentFrom 同 BackupCurrent，但备份指定路径 srcExe（供测试指向假二进制）。
// 复制（而非移动）srcExe 到 <backupDir>/binary——当前二进制仍在运行不能移走；
// 用「临时文件 + rename」原子落地，避免半截备份。同写 meta.json。
func BackupCurrentFrom(component, currentVersion, srcExe string, root *dataroot.Root) error {
	dir := backupDir(component, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建备份目录失败: %w", err)
	}

	binPath := filepath.Join(dir, backupBinaryName)
	tmp := binPath + ".tmp"
	if err := copyFile(srcExe, tmp, 0o755); err != nil {
		return fmt.Errorf("复制当前二进制到备份失败: %w", err)
	}
	sum, err := FileSHA256(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("计算备份 sha256 失败: %w", err)
	}
	if err := os.Rename(tmp, binPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("落地备份二进制失败: %w", err)
	}

	meta := BackupMeta{Version: currentVersion, SHA256: sum, BackedUpAt: time.Now()}
	if err := writeBackupMeta(dir, meta); err != nil {
		return err
	}
	return nil
}

// BackupInfo 只读查组件备份元数据；无备份（meta.json 缺失/损坏）返回 (零值, false)。
// 供「检查更新」透出 backupVersion 与前端判定回滚按钮可用性。
func BackupInfo(component string, root *dataroot.Root) (BackupMeta, bool) {
	meta, err := readBackupMeta(backupDir(component, root))
	if err != nil {
		return BackupMeta{}, false
	}
	return meta, true
}

// Rollback 把当前进程的可执行文件换回组件备份（FR-182，见 ADR-042）。
// 流程：读 meta（无 → ErrNoBackup）→ 校验备份二进制 sha256 与 meta 一致（防损坏）→
// 复制备份到临时文件 → ReplaceExecutable 换回 → 返回 meta（含回滚到的版本）。
// 不在此重启——与升级一致，替换成功后由调用方异步延迟重启。
func Rollback(component string, root *dataroot.Root) (BackupMeta, error) {
	target, err := os.Executable()
	if err != nil {
		return BackupMeta{}, fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	return RollbackTo(component, target, root)
}

// RollbackTo 同 Rollback，但替换指定目标 target（供测试指向假二进制）。
func RollbackTo(component, target string, root *dataroot.Root) (BackupMeta, error) {
	dir := backupDir(component, root)
	meta, err := readBackupMeta(dir)
	if err != nil {
		return BackupMeta{}, ErrNoBackup
	}
	binPath := filepath.Join(dir, backupBinaryName)
	got, err := FileSHA256(binPath)
	if err != nil {
		return BackupMeta{}, ErrNoBackup
	}
	if got != meta.SHA256 {
		// 备份在磁盘上被损坏/篡改：绝不把坏二进制换上去。
		return BackupMeta{}, fmt.Errorf("%w: 备份二进制已损坏（sha256 与记录不符）", ErrChecksumMismatch)
	}

	// 复制备份到同目录临时文件再替换（保留备份原地，支持重复回滚）；同目录确保 ReplaceExecutable 的 rename 原子。
	stage := target + ".rollback"
	if err := copyFile(binPath, stage, 0o755); err != nil {
		return BackupMeta{}, fmt.Errorf("准备回滚二进制失败: %w", err)
	}
	if err := ReplaceExecutable(target, stage); err != nil {
		_ = os.Remove(stage)
		return BackupMeta{}, err
	}
	return meta, nil
}

// copyFile 复制 src 到 dst（覆盖），并设置权限 perm。
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// writeBackupMeta 把 meta 写入 <dir>/meta.json（临时文件 + rename 原子落地）。
func writeBackupMeta(dir string, meta BackupMeta) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化备份元数据失败: %w", err)
	}
	metaPath := filepath.Join(dir, backupMetaName)
	tmp := metaPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入备份元数据失败: %w", err)
	}
	if err := os.Rename(tmp, metaPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("落地备份元数据失败: %w", err)
	}
	return nil
}

// readBackupMeta 读取并解析 <dir>/meta.json。
func readBackupMeta(dir string) (BackupMeta, error) {
	raw, err := os.ReadFile(filepath.Join(dir, backupMetaName))
	if err != nil {
		return BackupMeta{}, err
	}
	var meta BackupMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return BackupMeta{}, err
	}
	if meta.Version == "" && meta.SHA256 == "" {
		return BackupMeta{}, errors.New("备份元数据为空")
	}
	return meta, nil
}
