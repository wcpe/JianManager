// Package tailer 是日志采集层的**重建实现**，完整对齐 Fluent Bit / Filebeat 模型。
//
// # 为什么重建而不是改造现有实现
//
// 现有 `internal/worker/logs/ingest` 的模型是「一个源 = 一个文件 = 四水位账本 + 缺口追踪」，
// 而这套模型有两处与业界实践根本不同：
//
//  1. **键是「源」不是「文件」**。于是“一个源采多个文件”需要改整个水位体系；
//  2. **它试图回答「这段数据是否完整」**。为此养了 3097 行账本 + 8600 行 recovery/gap/persist。
//
// Fluent Bit 与 Filebeat 不这么做。它们的模型只有三层：
//
//	prospector（扫描） → harvester（每文件一个） → registry（file_identity → offset）
//
// 所有文件**平级**，各自独立记账；日志离开采集器时只带一个 tag，
// 采集器不再关心“这份数据属不属于某个源的完整集合”。
//
// # 官方依据（每一项都能在文档里找到）
//
// Fluent Bit Tail（https://docs.fluentbit.io/manual/pipeline/inputs/tail）：
//   - `path`：Pattern specifying a specific log file or multiple ones through common wildcards
//   - `db`：database file to keep track of monitored files and offsets；**stores `inode=offset`**
//   - `exclude_path`：e.g. `*.gz,*.zip`
//   - `refresh_interval`：默认 60s；`progress_check_interval`：默认 2s
//   - `read_from_head` / `read_newly_discovered_files_from_head`
//   - `db.sync`：默认 `normal`（**不做 fsync**）
//
// Filebeat filestream（https://www.elastic.co/guide/en/beats/filebeat/current/filebeat-input-filestream.html）：
//   - `paths`：glob-based；"Filebeat starts a harvester for each file that it finds"
//   - `file_identity: fingerprint`：hash 文件前 1024 字节。
//     文档明确指出它**solves data duplication caused by inode reuse**——这是我们选它的硬理由。
//   - `exclude_files`：e.g. `['\.gz$']`
//   - `close.on_state_change.inactive`：默认 5m
//   - `clean_removed`：文件消失后清理位点
//   - `harvester_limit`：并发上限
//
// # 与旧实现的关系
//
// 本包**不 import** `ingest` / `ledger` / `catalog`；它是平行的新实现。
// 阶段一可独立单测；阶段二并行接入验证；阶段三旧代码删除。
package tailer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FingerprintLength 是文件指纹的长度（Filebeat 默认 1024 字节）。
//
// 为什么不取更短：指纹越短，内容相近的文件（如同模板的两个实例日志）
// 碰撞概率越高，碰撞的后果是「把 B 文件当成 A 读过的那一个」，会错位续读。
// 为什么不取更长：每次扫描都要读它，而扫描是周期性的（默认 60s）。
// 1024 字节是官方默认值，在“能区分”与“便宜”之间已经调好。
const FingerprintLength = 1024

// FileIdentity 是文件的**稳定身份**，用于在 registry 里定位它的位点。
//
// # 为什么用指纹而不是 inode
//
// Filebeat 文档原文：
//
//	The default behavior is to identify files based on their contents using
//	`fingerprint`. This solves data duplication caused by inode reuse.
//
// inode 会被复用（文件删了之后新文件可能拿到同一个 inode），只靠它会让
// “新文件”被误认为“已读过”，从而从旧 offset 继续——那是**静默丢数据**。
//
// 指纹（文件前 1KB 的 hash）不依赖文件系统，也不惧 inode 复用；
// 轮转时文件被改名，内容不变 ⇒ 指纹不变 ⇒ 自动跟上，这正是我们要的。
//
// # 为什么保留 Inode/Dev
//
// 它们**只用于诊断**（“这个 fingerprint 对应的文件被换了”），以及作为
// 指纹不可用时的兜底（文件小于 FingerprintLength 且内容全同的极端情况）。
// 判定文件身份时以 Fingerprint 为准。
type FileIdentity struct {
	// Fingerprint 是 sha256(文件前 FingerprintLength 字节) 的十六进制。
	// 空字符串表示“算不出来”（文件不可读/不足长度且未开启 growing），
	// 此时 fallbackToInode 决定行为。
	Fingerprint string
	Inode       uint64
	Dev         uint64
}

// Key 返回用于 registry 索引的稳定键。
//
// 优先指纹；指纹缺失时退到 inode+dev（并带前缀区分，避免与指纹值混淆）。
func (f FileIdentity) Key() string {
	if f.Fingerprint != "" {
		return "fp:" + f.Fingerprint
	}
	return fmt.Sprintf("ino:%d:%d", f.Dev, f.Inode)
}

// String 便于日志输出。
func (f FileIdentity) String() string {
	if f.Fingerprint != "" {
		return "fp:" + f.Fingerprint[:min(12, len(f.Fingerprint))]
	}
	return fmt.Sprintf("ino:%d:%d", f.Dev, f.Inode)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// IdentifyFile 计算某文件的身份。
//
// # 为什么指纹读失败不直接算错
//
// 文件可能恰好在这时候被删/被改（轮转瞬间）。此时：
//   - 返回带 inode 的 identity + err，让调用方决定“当新文件还是跳过”；
//   - 不返回零值 identity——那会让调用方把“不可读”当成“同一身份”，更危险。
func IdentifyFile(path string) (FileIdentity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return FileIdentity{}, err
	}
	id := FileIdentity{}
	if ino, dev, ok := inodeOf(fi); ok {
		id.Inode, id.Dev = ino, dev
	}
	fp, err := fingerprintOf(path, fi.Size())
	if err != nil {
		// 指纹算不出来时返回已拿到的 inode 部分 + 错误，由调用方处置。
		return id, err
	}
	id.Fingerprint = fp
	return id, nil
}

// fingerprintOf 读文件前 FingerprintLength 字节并哈希。
//
// # 稳定性边界（必读，2026-11-06 实测踩到）
//
// 指纹只对**前 FingerprintLength 字节**取哈希，所以：
//
//	文件 < 1024 字节：追加内容 ⇒ 参与哈希的字节变多 ⇒ **指纹会变**
//	文件 ≥ 1024 字节：追加内容 ⇒ 前 1024 字节不变 ⇒ **指纹不变**
//
// 这是 Filebeat 文档明确写的语义：
//
//	By default, the hashed part of the file is the first 1024 bytes
//	(offset: 0, length: 1024). ... This makes sure that a produced
//	fingerprint is a stable unique identifier for each file.
//
// 而小文件的问题由它的 `growing fingerprint` 机制处理（见下）。
//
// # 为什么不直接“对全部内容哈希”
//
// 那样每次追加都会得到新指纹 ⇒ registry 永远命中不到 ⇒ **每次重启都从头重读**。
// 这正是本文件最初写错的地方（把 buf[:read] 但 read 随文件长度变）。
// 正确做法是**固定读 1024 字节**，不足则有多少读多少——
// 关键是：文件长度不变时指纹稳定；长度超过 1024 后长度再变指纹也不变。
//
// # growing 行为（对齐 Filebeat `file_identity.fingerprint.growing`）
//
// Filebeat 9.5+ 默认开启 growing：文件不足指纹长度时用“现有字节”算指纹，
// 文件长大到指纹长度后，身份会迁移到正式指纹上（文档：
// "its identity becomes the regular SHA-256 fingerprint ... and its registry
// entry is migrated automatically. No data is re-ingested"）。
//
// 我们的取舍：**不做身份迁移**（那需要额外的迁移逻辑与原子性保证），
// 而是接受一个前提：**小于 1024 字节的文件在被追加时会重读**。
// 这在实际场景里代价极小：
//
//   - 生产 MC 日志文件都是 MB 级，远超 1KB；
//   - 就算重读，也只是重读一个不足 1KB 的文件，代价微秒级。
//
// 如果将来要支持小文件的高频写入场景，再补迁移逻辑。
func fingerprintOf(path string, _ int64) (string, error) {
	return fingerprintOfFixed(path, FingerprintLength)
}

// fingerprintOfFixed 对文件前 n 字节算指纹（n 固定时，指纹只与那 n 字节有关）。
//
// # 为何把长度参数化
//
// 判定「同一文件的延长」时（见 growing.go），必须对**记录当时**的字节数重算；
// 而默认身份用固定 1024。两者必须走同一算法（仅长度不同），否则不可比。
//
// 为什么不是“哈希全部内容”：那样每次追加都会得到新指纹 ⇒ registry 永远命中不到
// ⇒ **每次重启都从头重读**。这正是本文件最初写错的地方（把 buf[:read] 而 read 随长度变）。
func fingerprintOfFixed(path string, n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// 文件比 n 短时 ReadFull 返回 ErrUnexpectedEOF，用已读到的字节算。
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	sum := sha256.Sum256(buf[:read])
	return hex.EncodeToString(sum[:]), nil
}

// CollectorConfig 是采集配置（对齐 Filebeat 的 input 配置 + Fluent Bit 的 Tail 配置）。
type CollectorConfig struct {
	// ID 对应 Filebeat 的 `id`。
	//
	// 官方原文："Each filestream input must have a unique ID. Omitting or changing
	// the filestream ID may cause data duplication."
	//
	// 我们不强制它唯一（内部用），但保留字段以便将来多个 collector 并存时隔离 registry。
	ID string

	// Paths 是 glob 列表（Filebeat `paths` / Fluent Bit `path`）。
	// 支持 `*` 与 `**`（递归）。
	Paths []string

	// Exclude 是排除规则（Filebeat `exclude_files` / Fluent Bit `exclude_path`）。
	//
	// 我们的默认值必须含 `*.gz`：用户明确要求不采归档；
	// 且 Filebeat 文档也把 `exclude_files: ['\.gz$']` 作为标准写法。
	Exclude []string

	// ScanInterval 是扫描新文件的间隔（Filebeat `prospector.scanner.check_interval`，默认 10s；
	// Fluent Bit `refresh_interval` 默认 60s）。
	//
	// 我们取 10s：MC 日志轮转频繁，60s 太慢（轮转后最多 60s 才发现新文件）；
	// 而扫描只是 Glob + Stat，10s 一次的代价可忽略。
	ScanInterval time.Duration

	// ProgressInterval 是轮询活跃文件新内容的间隔（Fluent Bit `progress_check_interval`，默认 2s）。
	ProgressInterval time.Duration

	// ReadFromHead 决定「首次发现的文件」从哪里开始读（Filebeat `read_from_head`/
	// Fluent Bit `read_from_head`）。
	//
	// true  ⇒ 从头读（能补上部署前的历史，是我们要的默认）
	// false ⇒ 从末尾读（只采新内容，经典 tail 行为）
	ReadFromHead bool

	// HarvesterLimit 是同时活跃的 harvester 上限（Filebeat `harvester_limit`）。
	//
	// <=0 表示不限。设上限的意义：文件很多时避免同时打开过多 fd 与占用过多内存。
	// Filebeat 未设时默认不限（它的 limit 靠 output 背压间接生效）。
	HarvesterLimit int

	// CloseInactive 是「多久没有新内容就关闭 reader」（Filebeat `close.on_state_change.inactive`，默认 5m）。
	//
	// 关闭 reader 不等于忘记位点：registry 里的 offset 一直在，下次扫描到它再打开继续。
	CloseInactive time.Duration

	// RegistryPath 是位点表路径（Fluent Bit `db`）。
	RegistryPath string
}

// EffectiveIntervals 返回生效的扫描/读取间隔（含默认值）。
//
// 导出它是因为装配层要在启动日志里打出「实际生效的值」——
// 运维排障时最需要知道真实值，而不是配置里写了什么。
func (c CollectorConfig) EffectiveIntervals() (scan, progress time.Duration) {
	d := c.withDefaults()
	return d.ScanInterval, d.ProgressInterval
}

// EffectiveExclude 返回生效的排除规则（含默认值）。
func (c CollectorConfig) EffectiveExclude() []string {
	return c.withDefaults().Exclude
}

// withDefaults 补默认值（不改调用方传进来的结构体本身，返回副本）。
func (c CollectorConfig) withDefaults() CollectorConfig {
	if c.ScanInterval <= 0 {
		c.ScanInterval = 10 * time.Second
	}
	if c.ProgressInterval <= 0 {
		c.ProgressInterval = 2 * time.Second
	}
	if c.CloseInactive <= 0 {
		c.CloseInactive = 5 * time.Minute
	}
	if len(c.Exclude) == 0 {
		// 默认排除归档与锁文件——这是用户明确要求的行为（不采 gz），
		// 也是 Filebeat 文档里给出的标准写法。
		c.Exclude = []string{"*.gz", "*.lck", "*.zip", "*.tmp"}
	}
	return c
}

// excluded 报告某路径是否被排除规则命中。
//
// 匹配**文件名**而不是全路径：用户写 `*.gz` 的意图是“所有 gz 都不采”，
// 而不是“只在根目录的 gz”。与 Filebeat 的 exclude_files（对文件名做正则）同精神。
func (c CollectorConfig) excluded(path string) bool {
	base := filepath.Base(path)
	for _, pat := range c.Exclude {
		// 路径模式里带 / 的按全路径匹配，否则只匹配文件名。
		target := base
		if strings.ContainsRune(pat, filepath.Separator) || strings.Contains(pat, "/") {
			target = path
		}
		if ok, err := filepath.Match(pat, target); err == nil && ok {
			return true
		}
	}
	return false
}
