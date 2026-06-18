package service

import (
	"strings"

	"github.com/google/uuid"
)

// allocWorkDirRel 为实例在数据根 var/servers 下系统分配一个工作目录，
// 返回相对数据根、以「/」分隔的可移植相对路径（var/servers/<slug>-<shortid>）。
//
// 工作目录由系统分配、不接受用户手填绝对路径（参见 ADR-007/ADR-010）：
//   - slug 取自实例名做安全化（小写、仅 [a-z0-9-]，截断），保证人工可读；
//   - shortid 取一段随机十六进制，保证唯一，避免重名实例冲突。
//
// 以相对路径登记是便携性的关键：数据根整体拷到另一机器后，路径仍自洽。
func allocWorkDirRel(name string) string {
	slug := slugify(name)
	if slug == "" {
		slug = "instance"
	}
	short := strings.ReplaceAll(uuid.New().String(), "-", "")[:8]
	return "var/servers/" + slug + "-" + short
}

// slugify 把任意名称转为安全的目录 slug：小写，非 [a-z0-9] 折叠为单个 '-'，
// 去除首尾 '-'，并限制最大长度，避免超长或非法字符进入文件系统。
func slugify(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	const maxLen = 48
	if len(s) > maxLen {
		s = strings.Trim(s[:maxLen], "-")
	}
	return s
}
