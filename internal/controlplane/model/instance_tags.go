package model

import (
	"encoding/json"
	"strings"
)

// EnvTagPrefix 是「环境维度」复用 Tags 字段的约定前缀（FR-047）。
// 不新增独立字段/迁移：环境用 `env:dev` / `env:test` / `env:prod` 形式的标签表达，
// 一个实例至多取首个 env 标签作为其环境，其余 env 标签忽略（避免歧义）。
const EnvTagPrefix = "env:"

// ParseTags 把实例持久化的 Tags（JSON 字符串数组）解析为规范化切片。
// 兼容空串/非法 JSON：返回空切片而非报错，避免历史脏数据阻断列表查询。
// 规范化：去首尾空白、丢弃空项、去重保序——与下方 NormalizeTags 行为一致，
// 保证「读出来再写回去」幂等。
func ParseTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil
	}
	return NormalizeTags(tags)
}

// NormalizeTags 规范化标签集合：去首尾空白、丢弃空项、去重并保持首次出现顺序。
// 大小写敏感（与 Network 群组名一致），不做小写折叠。
func NormalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// EnvFromTags 返回实例的环境维度值（去掉 `env:` 前缀后的部分），取首个 env 标签。
// 无 env 标签时返回空串，表示「未分环境」。
func EnvFromTags(tags []string) string {
	for _, t := range tags {
		if strings.HasPrefix(t, EnvTagPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(t, EnvTagPrefix))
		}
	}
	return ""
}

// MatchEnv 判断实例标签是否属于指定环境。
// env 传空串表示不按环境过滤（恒为真）；否则要求实例的首个 env 标签等于 env。
func MatchEnv(tags []string, env string) bool {
	env = strings.TrimSpace(env)
	if env == "" {
		return true
	}
	return EnvFromTags(tags) == env
}

// MatchTag 判断实例标签集合是否精确包含指定标签。
// tag 传空串表示不按标签过滤（恒为真）。用于「按任意标签筛选」，
// 区别于环境维度（环境只认 env: 前缀的首个值）。
func MatchTag(tags []string, tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return true
	}
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
