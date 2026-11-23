package tailer

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoveredFile 是一次扫描发现的文件。
type DiscoveredFile struct {
	Path     string
	Size     int64
	ModTime  int64
	Identity FileIdentity
}

// Patterns 返回当前生效的采集 glob（含运行期追加项）。
//
// # 为何路径可以动态加
//
// 实例的工作目录由 CP 在**运行期**逐个下发（建实例 / 重启后重注册），启动时
// 不可能知道全量。若只认启动时那批 glob，新建实例的日志要等到 Worker 重启才被采——
// 而 Worker 可能几个月不重启，那段时间的日志就静默地不存在。
//
// 所以按需读取额外路径：只在本函数里合并，扫描本身仍是无状态纯函数。
func (c CollectorConfig) Patterns() []string {
	if c.ExtraPaths == nil {
		return c.Paths
	}
	extra := c.ExtraPaths()
	if len(extra) == 0 {
		return c.Paths
	}
	out := make([]string, 0, len(c.Paths)+len(extra))
	out = append(out, c.Paths...)
	out = append(out, extra...)
	return out
}

// Scan 执行一次 glob 扫描，返回当前匹配且未被排除的文件。
//
// # 对齐官方设计
//
// Filebeat："Filebeat starts a harvester for each file that it finds under the
// specified paths"——扫描与采集分离，扫描只负责回答“现在有哪些文件”。
//
// Fluent Bit：`path` 支持逗号分隔多个 pattern；`exclude_path` 做排除。
//
// # 为什么扫描不保留状态
//
// 扫描是无状态的纯函数：同样的目录内容 ⇒ 同样的返回。状态（哪些已读过、读到哪）
// 全在 registry 里。这样扫描可以任意重入、并发、单测。
//
// # `**` 递归的处理
//
// Go 的 filepath.Glob **不支持 `**`**。Filebeat 的 recursive_glob 是它自己实现的。
//
// 我们的取舍：不实现 `**`，要求调用方用 `dir/*/*.log` 这种显式形式。
// 理由：本项目实际用到的 glob 都是固定深度（`servers/*/logs/*.log`）；
// 自行展开 `**` 会引入“展开多深才够”这个无法回答的问题。
// 如果调用方传了 `**`，按字面量 glob 处理（会匹配到名字里真含 `**` 的文件），
// 并在日志里告警——不静默地丢掉这条规则。
// ScanGzip 扫描 `.gz` 归档（只在 IncludeGzip 时使用）。
//
// # 为何不能复用 Scan
//
// `Scan` 会应用 Exclude（默认含 `*.gz`），而这里的意图正好相反：**只**要 gz。
// 用同一函数再传一组“相反的排除规则”会让语义绕一圈，不如显式分开。
//
// # 为何不按“gzip 内容”判定
//
// 扫描期要对每个候选文件读 2 字节验魔数，而生产有 1374 个 gz、且每轮都要扫。
// 按扩展名筛是廉价的第一层；真正的魔数校验在 OpenGzip 里做（打不开就跳过并告警）。
func (c CollectorConfig) ScanGzip() ([]DiscoveredFile, error) {
	if !c.IncludeGzip {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var out []DiscoveredFile
	for _, pattern := range c.Patterns() {
		// 采集 glob 通常写 `*.log`；要覆盖归档得同时试 `*.log.gz`。
		// 这里把 pattern 的最后一个 `*` 后缀替换成 `*.gz` 的两种常见形态。
		for _, gzPattern := range gzipPatternsOf(pattern) {
			matches, err := filepath.Glob(gzPattern)
			if err != nil {
				continue
			}
			for _, path := range matches {
				if _, dup := seen[path]; dup {
					continue
				}
				if !IsGzipPath(path) || c.excluded(path) {
					continue
				}
				info, statErr := os.Stat(path)
				if statErr != nil || info.IsDir() {
					continue
				}
				identity, idErr := IdentifyFile(path)
				if idErr != nil && identity.Fingerprint == "" && identity.Inode == 0 {
					continue
				}
				seen[path] = struct{}{}
				out = append(out, DiscoveredFile{
					Path:     path,
					Size:     info.Size(),
					ModTime:  info.ModTime().UnixNano(),
					Identity: identity,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// gzipPatternsOf 由一个采集 glob 推出可能的 gz 归档 glob。
//
// 两种真实形态：
//
//	<dir>/logs/*.log   ⇒ <dir>/logs/*.log.gz（log4j2 的 `%d-...log.gz`）与 <dir>/logs/*.gz
//	<dir>/proxy.log.*  ⇒ <dir>/proxy.log.*.gz（少数核型）
func gzipPatternsOf(pattern string) []string {
	out := []string{pattern + ".gz"}
	if i := strings.LastIndex(pattern, "*"); i >= 0 {
		// 把 `*.log` 变成 `*.gz`：覆盖「归档文件名不带 .log」的核型。
		out = append(out, pattern[:i+1]+".gz")
	}
	return out
}

func (c CollectorConfig) Scan() ([]DiscoveredFile, error) {
	seen := make(map[string]struct{})
	var out []DiscoveredFile
	patterns := c.Patterns()
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			// 单个 pattern 语法错不拖垮整体：其余 pattern 照常扫。
			// （Filebeat 对无效 glob 也是告警后继续）
			continue
		}
		for _, path := range matches {
			if _, dup := seen[path]; dup {
				continue // 多个 pattern 可能重叠，去重
			}
			if c.excluded(path) {
				continue
			}
			info, statErr := os.Stat(path)
			if statErr != nil || info.IsDir() {
				continue // 轮转瞬间可能已消失；目录不是日志文件
			}
			identity, idErr := IdentifyFile(path)
			if idErr != nil && identity.Fingerprint == "" && identity.Inode == 0 {
				// 连 inode 都拿不到 ⇒ 文件真的不可读，跳过。
				continue
			}
			seen[path] = struct{}{}
			out = append(out, DiscoveredFile{
				Path:     path,
				Size:     info.Size(),
				ModTime:  info.ModTime().UnixNano(),
				Identity: identity,
			})
		}
	}
	// 排序：让调度顺序稳定（便于测试与排障）。按路径字典序，
	// 不按 mtime——mtime 会随时变，排序结果不稳定。
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// HasRecursiveGlob 报告配置里是否含 `**`。
//
// 供装配层启动时告警用：本项目不支持 `**`，静默不生效比报错更坑。
func (c CollectorConfig) HasRecursiveGlob() bool {
	for _, p := range c.Paths {
		if strings.Contains(p, "**") {
			return true
		}
	}
	return false
}
