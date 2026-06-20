package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseTags(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"空串", ``, nil},
		{"空白", `   `, nil},
		{"非法 JSON", `not-json`, nil},
		{"空数组", `[]`, nil},
		{"正常", `["env:prod","survival"]`, []string{"env:prod", "survival"}},
		{"去空白去空项", `[" a "," ","b"]`, []string{"a", "b"}},
		{"去重保序", `["a","b","a","c","b"]`, []string{"a", "b", "c"}},
		{"全空白项→nil", `["  ",""]`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ParseTags(tt.raw))
		})
	}
}

func TestNormalizeTags(t *testing.T) {
	assert.Nil(t, NormalizeTags(nil))
	assert.Nil(t, NormalizeTags([]string{}))
	assert.Equal(t, []string{"a", "b"}, NormalizeTags([]string{" a ", "b", "a", ""}))
	// 大小写敏感：Prod 与 prod 视为不同标签
	assert.Equal(t, []string{"Prod", "prod"}, NormalizeTags([]string{"Prod", "prod"}))
}

func TestEnvFromTags(t *testing.T) {
	assert.Equal(t, "", EnvFromTags(nil))
	assert.Equal(t, "", EnvFromTags([]string{"survival"}))
	assert.Equal(t, "prod", EnvFromTags([]string{"survival", "env:prod"}))
	// 多个 env 标签取首个
	assert.Equal(t, "dev", EnvFromTags([]string{"env:dev", "env:prod"}))
	assert.Equal(t, "test", EnvFromTags([]string{"env: test "}))
}

func TestMatchEnv(t *testing.T) {
	tags := []string{"env:prod", "survival"}
	assert.True(t, MatchEnv(tags, ""))      // 空过滤恒真
	assert.True(t, MatchEnv(tags, "prod"))  // 命中
	assert.False(t, MatchEnv(tags, "dev"))  // 不命中
	assert.False(t, MatchEnv(nil, "prod"))  // 无环境标签
	assert.True(t, MatchEnv(tags, "  "))    // 仅空白视为空过滤
}

func TestMatchTag(t *testing.T) {
	tags := []string{"env:prod", "survival", "eu"}
	assert.True(t, MatchTag(tags, ""))          // 空过滤恒真
	assert.True(t, MatchTag(tags, "survival"))  // 普通标签命中
	assert.True(t, MatchTag(tags, "env:prod"))  // env 标签也可作普通标签精确命中
	assert.False(t, MatchTag(tags, "asia"))     // 不命中
	assert.False(t, MatchTag(nil, "survival"))  // 空集合
}
