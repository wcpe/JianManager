//go:build !windows

package daemon

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListen_RejectsOverlongSocketPath 守护「超长 socket 路径给出可读诊断」。
//
// 现场背景（2026-10-26 真机）：socket 路径由 <data_dir>/var/servers/<uuid>.sock 拼成，
// uuid 固定 36 字节，部署目录一深即超出 sockaddr_un.sun_path 上限。内核只回
// "bind: invalid argument"，不指向根因，表现是实例反复 CRASHED 且无从判断。
//
// 本测试要求错误信息同时满足三点，缺一即无法据此定位：
//  1. 指出路径过长（而不是把内核原文抛给上层）；
//  2. 给出实际长度与上限；
//  3. 给出应缩短什么（data_dir / 部署目录层级）。
func TestListen_RejectsOverlongSocketPath(t *testing.T) {
	// 构造一个确定超限的路径：目录名足够长即可，无需真实存在（预检先于任何 IO）。
	longDir := filepath.Join(t.TempDir(), strings.Repeat("d", unixSocketPathMax))
	addr := filepath.Join(longDir, "x.sock")
	require.Greater(t, len(addr), unixSocketPathMax, "用例前提：路径必须超过上限")

	_, err := Listen(addr)
	require.Error(t, err, "超长路径必须被拒绝")

	msg := err.Error()
	assert.Contains(t, msg, "socket 路径过长", "必须点明根因")
	assert.Contains(t, msg, fmt.Sprint(len(addr)), "必须给出实际字节数")
	assert.Contains(t, msg, fmt.Sprint(unixSocketPathMax), "必须给出上限")
	assert.Contains(t, msg, "data_dir", "必须指出可调整的配置项")
}

// TestListen_AcceptsPathAtLimit 守护边界：恰好等于上限的路径应放行到真实 bind。
//
// 只断言「未被长度预检拦截」：路径本身可能因目录不存在而 bind 失败，那属另一类错误。
func TestListen_AcceptsPathAtLimit(t *testing.T) {
	dir := t.TempDir()
	// 把总长度精确补到上限。
	name := ".sock"
	pad := unixSocketPathMax - len(dir) - 1 - len(name)
	require.Greater(t, pad, 0, "临时目录过长，无法构造边界用例")
	addr := filepath.Join(dir, strings.Repeat("a", pad)+name)
	require.Equal(t, unixSocketPathMax, len(addr), "用例前提：路径恰好等于上限")

	ln, err := Listen(addr)
	if err != nil {
		assert.NotContains(t, err.Error(), "socket 路径过长", "恰好等于上限不应被长度预检拦截")
		return
	}
	ln.Close()
}

// TestSocketAddr_OverlongPidDirProducesActionableError 端到端串起真实调用链：
// 深层 data_dir → SocketAddr → Listen 报可读错误。
func TestSocketAddr_OverlongPidDirProducesActionableError(t *testing.T) {
	deepPidDir := filepath.Join(t.TempDir(), strings.Repeat("x", unixSocketPathMax))
	addr := SocketAddr(deepPidDir, "fbd5a26d-5ae3-46be-b5e9-56f07a935fc7")
	require.Greater(t, len(addr), unixSocketPathMax)

	_, err := Listen(addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "socket 路径过长")
}
