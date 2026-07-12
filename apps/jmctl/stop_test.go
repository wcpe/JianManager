package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseTargetArgs 解析「一个位置参数（uuid 前缀）+ 可选 --pid-dir」，
// 关键：位置参数与 flag 的先后顺序不限（stdlib flag 在遇到首个非 flag 即停止解析，
// 故 `stop <uuid> --pid-dir X` 这种自然写法必须被支持）。
func TestParseTargetArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantPrefix string
		wantPIDDir string
		wantErr    bool
	}{
		{"仅前缀", []string{"abc"}, "abc", "", false},
		{"flag 在前缀前", []string{"--pid-dir", "/d", "abc"}, "abc", "/d", false},
		{"flag 在前缀后", []string{"abc", "--pid-dir", "/d"}, "abc", "/d", false},
		{"flag 等号形式在后", []string{"abc", "--pid-dir=/d"}, "abc", "/d", false},
		{"缺前缀", []string{"--pid-dir", "/d"}, "", "", true},
		{"多余位置参数", []string{"abc", "def"}, "", "", true},
		{"空", []string{}, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix, pidDir, err := parseTargetArgs("stop", tt.args)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPrefix, prefix)
			assert.Equal(t, tt.wantPIDDir, pidDir)
		})
	}
}
