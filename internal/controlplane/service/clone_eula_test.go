package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 真机复现（FR-036/231）：快速复制的 include 集漏掉 eula.txt，克隆出的子服首次启动
// 被 Paper 以自生成的 eula=false 拒启（实例直接 CRASHED），用户必须手动补签 EULA。
// 源实例已同意的 EULA 属于「能开起来」的根配置，必须随快速复制带走。
func TestQuickCloneIncludes_EulaCarriedOver(t *testing.T) {
	require.Contains(t, quickCloneIncludes, "eula.txt",
		"快速复制必须包含 eula.txt，否则克隆服首启必崩（EULA 未同意）")
}
