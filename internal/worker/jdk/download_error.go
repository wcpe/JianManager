package jdk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// networkHint 是网络类下载失败追加的可操作引导（FR-279）。
// JDK 官方源（GitHub/foojay 等）在受限网络下 TLS 握手超时是常态，裸错误让运维无从下手；
// 明示两条既有出路：节点出站代理（面板「设置 → 网络」下发，ADR-043）与 JDK 下载镜像源（「运行时资产」页，FR-178 mirrorBase）。
const networkHint = "（疑似网络受限：JDK 下载经节点出站代理执行、未配置则直连，可在面板「设置 → 网络」配置出站代理，或在「运行时资产」页更换 JDK 下载源/镜像后重试）"

// isNetworkError 判定 err 是否为「到不了下载源」的网络类错误（超时 / 连接被拒 / DNS / TLS 握手等）。
// 用于对 JDK 下载失败追加可操作引导（FR-279），与「归档损坏 / 磁盘满」等本地类错误区分。
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	// stall 看门狗 / 强制停止把 context 取消归为「卡死无进展」，同属网络不可达语义。
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	// TLS 握手超时等被 net/http 包成普通 error 字符串，无稳定类型可断言，按 Go stdlib 稳定标记兜底匹配。
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"tls handshake timeout",
		"i/o timeout",
		"deadline exceeded",
		"connection refused",
		"no such host",
		"network is unreachable",
		"connection reset",
		"dial tcp",
		"eof", // 代理/中间层截断
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// annotateDownloadError 对网络类下载失败追加可操作引导（FR-279）；非网络类原样返回。
// 保留底层错误（含 Go stdlib 英文标记，如 "TLS handshake timeout"），前端据其识别网络类失败加设置入口。
func annotateDownloadError(err error) error {
	if err == nil || !isNetworkError(err) {
		return err
	}
	// 用 %w 保留原错误链（errors.Is/As 不受影响），并保留底层英文标记供前端识别网络类失败。
	return fmt.Errorf("%w%s", err, networkHint)
}
