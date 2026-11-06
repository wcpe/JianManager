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
func (c CollectorConfig) Scan() ([]DiscoveredFile, error) {
	seen := make(map[string]struct{})
	var out []DiscoveredFile
	for _, pattern := range c.Paths {
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
