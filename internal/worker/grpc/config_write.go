package grpc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// 配置落盘与读回校验（FR-530）。
//
// 设计要点（对应 spec §3）：
//  1. 原子写入：同目录临时文件 → fsync → rename → fsync 目录。
//     直接 os.WriteFile 在写入中崩溃会留半截文件，插件启动时读到的就是坏配置。
//  2. 读回校验：写完重开文件读全量、算 sha256 与 CP 的 expectedHash 比对。
//     不一致说明磁盘有问题或有并发写，必须报 WRITE_VERIFY_FAILED 而不是谎报成功。
//  3. 路径越界检查：绝对路径、`..` 穿越、符号链接逃逸三重防护。
//  4. 写前快照：<workDir>/.jm-backup/<path>.<timestamp>，保留最近 N 个。
//  5. 并发保护：同实例同路径串行化，避免「两个写同时 rename」与快照被覆盖。

// 写入错误码（与 proto WriteConfigResponse.error_code 对齐）。
const (
	// ErrCodePathEscape 路径越界：绝对路径 / `..` 穿越 / 符号链接逃逸。
	ErrCodePathEscape = "PATH_ESCAPE"
	// ErrCodeWriteFailed 写盘失败（临时文件、fsync、rename 任一环节）。
	ErrCodeWriteFailed = "WRITE_FAILED"
	// ErrCodeWriteVerifyFailed 读回内容与 expectedHash 不一致。
	ErrCodeWriteVerifyFailed = "WRITE_VERIFY_FAILED"
	// ErrCodeBackupFailed 写前快照失败（保守起见整体失败，避免无回滚点）。
	ErrCodeBackupFailed = "BACKUP_FAILED"
	// ErrCodeValidationFailed 格式校验失败。
	ErrCodeValidationFailed = "VALIDATION_FAILED"
)

// 路径解析错误。
var (
	// ErrAbsolutePath 拒绝绝对路径：配置路径必须是相对工作目录的。
	ErrAbsolutePath = errors.New("不允许绝对路径")
	// ErrPathEscape 路径逃出工作目录。
	ErrPathEscape = errors.New("路径越界：不在工作目录内")
	// ErrSymlinkEscape 符号链接解析后逃出工作目录。
	ErrSymlinkEscape = errors.New("路径越界：符号链接指向工作目录外")
)

// defaultBackupRetention 写前快照保留数量（spec §3.4：最近 N 个，默认 10）。
const defaultBackupRetention = 10

// backupDirName 快照目录名（相对工作目录），与 spec §3.4 一致。
const backupDirName = ".jm-backup"

// resolveSafePath 把相对路径安全地解析为工作目录内的绝对路径。
//
// 三重防护：
//  1. 绝对路径直接拒绝（否则 filepath.Join 会「吞掉」workDir）；
//  2. `..` 穿越：Clean 后必须以 `..` 判断，且 Join 后的结果必须仍在 workDir 下；
//  3. 符号链接逃逸：对**已存在的祖先路径**做 EvalSymlinks 后二次校验。
//     目标文件本身可能还不存在（新建），所以从最深的已存在祖先开始校验。
func resolveSafePath(workDir, relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", fmt.Errorf("%w: 路径为空", ErrPathEscape)
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("%w: %s", ErrAbsolutePath, relPath)
	}
	clean := filepath.Clean(relPath)
	if clean == "." || clean == string(filepath.Separator) {
		return "", fmt.Errorf("%w: 不得为工作目录根", ErrPathEscape)
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, relPath)
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("解析工作目录失败: %w", err)
	}
	abs := filepath.Join(absWork, clean)
	// 字符串前缀校验（EvalSymlinks 之前的第一道）。
	if !strings.HasPrefix(abs, absWork+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, relPath)
	}
	// 符号链接二次校验：从目标向上找最深的已存在祖先，解析真实路径后重新比对。
	if err := verifyNoSymlinkEscape(absWork, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// verifyNoSymlinkEscape 检查 target 的最深已存在祖先在解析符号链接后是否仍在 workDir 内。
func verifyNoSymlinkEscape(absWork, target string) error {
	realWork, err := filepath.EvalSymlinks(absWork)
	if err != nil {
		// 工作目录本身解析不了（可能不存在）：无法证明安全，但这不是越界，
		// 交给后续的 MkdirAll/写入报错。这里只用原始 absWork 继续前缀校验。
		realWork = absWork
	}
	// 向上找到第一个存在的祖先（含 target 自身）。
	probe := target
	for {
		if _, statErr := os.Lstat(probe); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("检查路径失败: %w", statErr)
		}
		parent := filepath.Dir(probe)
		if parent == probe || parent == filepath.Dir(parent) {
			// 已到文件系统根仍未找到存在的路径：目标工作目录可能整条都不存在，
			// 交给上层 MkdirAll 处理（此时不存在符号链接逃逸的可能）。
			return nil
		}
		probe = parent
	}
	realProbe, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return fmt.Errorf("解析符号链接失败: %w", err)
	}
	if realProbe == realWork {
		// 祖先就是工作目录本身，安全。
		return nil
	}
	if !strings.HasPrefix(realProbe, realWork+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrSymlinkEscape, target)
	}
	return nil
}

// atomicWriteFile 原子写文件：临时文件 → fsync → rename → fsync 目录。
//
// 权限沿用 0644（与既有行为一致）；若目标已存在则沿用其权限位，
// 避免把运维特意改成 0600 的文件写回 0644。
func atomicWriteFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	// 临时文件必须与目标同目录，否则 rename 跨设备会退化（EXDEV）而失去原子性。
	tmpPath := path + ".tmp." + uuid.New().String()
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	// 任何一步失败都要清掉临时文件，不能在实例目录里留垃圾。
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := f.Write(content); err != nil {
		cleanup()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("fsync 临时文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("原子替换失败: %w", err)
	}
	// fsync 目录，保证 rename 本身持久化（否则掉电后可能只见旧文件或都不见）。
	syncDir(dir)
	return nil
}

// syncDir 尽力 fsync 目录。失败不视为致命：rename 已生效，
// 掉电时丢失的是「目录项持久化」的保证，而非文件内容；把写入整体判失败会造成更坏的误报。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// hashFile 读全量并计算 sha256（小写 hex）。
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("读取文件失败: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashBytes 计算内容 sha256（小写 hex）。
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeSnapshot 在写前备份原文件到 <workDir>/.jm-backup/<path>.<timestamp>。
//
// 返回快照的相对路径（相对工作目录）与是否创建。仅当原文件存在且内容与
// 即将写入的内容不同时才备份——重复写同样内容不需要消耗快照配额。
func writeSnapshot(workDir, absPath string, newContent []byte, retention int) (relPath string, created bool, err error) {
	old, readErr := os.ReadFile(absPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			// 目标不存在 = 新建，无「原文件」可备份。
			return "", false, nil
		}
		return "", false, fmt.Errorf("%w: 读取原文件失败: %v", ErrSnapshotRead, readErr)
	}
	if hashBytes(old) == hashBytes(newContent) {
		// 内容无变化：不产生快照（否则轮转会很快把有价值的历史挤掉）。
		return "", false, nil
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(absWork, absPath)
	if err != nil {
		return "", false, err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	snapshotAbs := filepath.Join(absWork, backupDirName, rel+"."+stamp)
	if err := os.MkdirAll(filepath.Dir(snapshotAbs), 0755); err != nil {
		return "", false, fmt.Errorf("创建快照目录失败: %w", err)
	}
	// 快照本身也走原子写：半截快照等于没有快照。
	if err := atomicWriteFile(snapshotAbs, old); err != nil {
		return "", false, fmt.Errorf("写快照失败: %w", err)
	}
	snapshotRel := filepath.Join(backupDirName, rel+"."+stamp)
	if retention > 0 {
		pruneSnapshots(filepath.Join(absWork, backupDirName), rel, retention)
	}
	return snapshotRel, true, nil
}

// ErrSnapshotRead 快照阶段读取原文件失败。
var ErrSnapshotRead = errors.New("快照读取失败")

// pruneSnapshots 清理某文件的多余快照，只保留最近 retention 个。
// 快照文件名形如 <rel>.<timestamp>，时间戳是 UTC 且定长，故字典序即时间序。
func pruneSnapshots(backupRoot, rel string, retention int) {
	dir := filepath.Join(backupRoot, filepath.Dir(rel))
	prefix := filepath.Base(rel) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) <= retention {
		return
	}
	sort.Strings(names)
	// 字典序升序 = 最旧在前；删掉前面多余的数量。
	for _, name := range names[:len(names)-retention] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// ---------------------------------------------------------------------------
// 并发保护
// ---------------------------------------------------------------------------

// pathWriteLocks 是实例级 + 路径 key 的写入串行化器。
//
// 为什么必须串行：两个写同时 rename 时后者覆盖前者，但两个 CP 请求都收到 success，
// 于是「最终内容」与「CP 认为已下发的内容」不一致；快照也会互相覆盖。
//
// 用 keyed mutex 而非全局锁：不同实例、不同路径之间不应互相阻塞。
type pathWriteLocks struct {
	mu    sync.Mutex
	locks map[string]*pathLockEntry
}

// pathLockEntry 单个 key 的锁与其引用计数（引用归零时删除，避免 map 无限增长）。
type pathLockEntry struct {
	mu   sync.Mutex
	refs int
}

// globalPathWriteLocks 进程级写入串行化表。Worker 单进程管理多实例，按 key 天然隔离。
var globalPathWriteLocks = &pathWriteLocks{locks: map[string]*pathLockEntry{}}

// lock 获取 key 的排他锁并返回解锁函数。
func (p *pathWriteLocks) lock(key string) func() {
	p.mu.Lock()
	entry, ok := p.locks[key]
	if !ok {
		entry = &pathLockEntry{}
		p.locks[key] = entry
	}
	entry.refs++
	p.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		p.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(p.locks, key)
		}
		p.mu.Unlock()
	}
}

// writeLockKey 构造「实例 + 路径」锁键。
// 用实例 UUID 而非工作目录：同一目录被两个实例共用是配置错误，
// 但按 UUID 分锁不会因此死锁，只是各自串行。
func writeLockKey(instanceUUID, absPath string) string {
	return instanceUUID + "\x00" + absPath
}
