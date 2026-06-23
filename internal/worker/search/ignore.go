package search

import (
	"path"
	"strings"
)

// 默认忽略规则（FR-074，见 ADR-017）。
//
// 这些是相对实例工作目录、以「/」分隔的可移植路径上的匹配规则，分三类：
//   - 目录前缀（以 / 结尾）：匹配该目录及其下所有内容（如 logs/）。
//   - basename glob（不含 /）：匹配任意层级文件的文件名（如 *.jar）。
//   - 路径片段：路径任一目录段精确等于它即忽略（如 .git）。
//
// 目的是把日志、缓存、运行态、二进制/归档、MC 世界数据等非源码内容排除出全文索引，
// 既省索引体积又避免把二进制塞进倒排。可经 Worker 配置 search.ignore 追加。
var defaultIgnore = []string{
	// 运行态 / 日志 / 缓存目录
	"logs/",
	"cache/",
	"crash-reports/",
	".git/",
	".svn/",
	"node_modules/",
	// MC 世界数据（海量二进制区块）。world、world_nether、world_the_end 等以 world 前缀。
	"world*/",
	// 二进制 / 归档 / 媒体扩展
	"*.jar",
	"*.zip",
	"*.gz",
	"*.tar",
	"*.rar",
	"*.7z",
	"*.class",
	"*.png",
	"*.jpg",
	"*.jpeg",
	"*.gif",
	"*.ico",
	"*.webp",
	"*.dat",
	"*.dat_old",
	"*.mca",
	"*.mcr",
	"*.nbt",
	"*.db",
	"*.db-wal",
	"*.db-shm",
	"*.lock",
	"*.pid",
	"*.bin",
	"*.so",
	"*.dll",
	"*.exe",
}

// matcher 把忽略规则集编译成可对路径快速判定的结构。
type matcher struct {
	dirPrefixes []string // 以 / 结尾的目录前缀（已去掉尾部 /，可含 glob，如 world*）
	baseGlobs   []string // basename glob（如 *.jar、config*.yml）
	segments    []string // 路径段精确匹配（如 .git）
}

// newMatcher 编译默认规则 + 追加规则（用户配置 search.ignore）。
// 追加规则与默认同语义：以 / 结尾视为目录前缀、含 . 的视为 basename glob、否则路径段。
func newMatcher(extra []string) *matcher {
	m := &matcher{}
	add := func(rules []string) {
		for _, raw := range rules {
			r := strings.TrimSpace(raw)
			if r == "" {
				continue
			}
			r = strings.TrimPrefix(r, "./")
			switch {
			case strings.HasSuffix(r, "/"):
				m.dirPrefixes = append(m.dirPrefixes, strings.TrimSuffix(r, "/"))
			case strings.ContainsAny(r, "*?[") || strings.Contains(r, "."):
				m.baseGlobs = append(m.baseGlobs, r)
			default:
				m.segments = append(m.segments, r)
			}
		}
	}
	add(defaultIgnore)
	add(extra)
	return m
}

// ignored 判定一个相对路径（以 / 分隔）是否应被忽略。
func (m *matcher) ignored(rel string) bool {
	rel = strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "./")
	if rel == "." || rel == "" {
		return false
	}
	segs := strings.Split(rel, "/")
	base := segs[len(segs)-1]

	// 路径段精确匹配（任一目录段命中即忽略，覆盖该段下全部内容）。
	for _, seg := range segs {
		for _, s := range m.segments {
			if seg == s {
				return true
			}
		}
	}

	// 目录前缀：rel 的某个祖先目录段（含 glob）匹配。
	for i := range segs {
		dir := segs[i]
		for _, p := range m.dirPrefixes {
			if ok, _ := path.Match(p, dir); ok {
				return true
			}
		}
	}

	// basename glob。
	for _, g := range m.baseGlobs {
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	return false
}
