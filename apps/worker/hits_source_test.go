package main

import (
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// TestHitsSourceAdapterNilClientDisablesRebuild 守住「没接 VL 时如实说没接」。
//
// # 为什么这条要单独立
//
// Catalog journal 自 42a1b728 起是**纯内存**的，所以「重启后历史天还能不能查到」
// 完全押在启动期 hits 重建上。装配层一旦漏接 HitsSource（2026-11-07 生产部署时
// 就是这个状态），重启后既无 journal 也无重建 ⇒ 历史天在查询侧**永久消失**，
// 而日志里只会有一句容易被人忽略的「未接 hits 数据源」。
//
// 这里钉住两件事：
//   1. nil 客户端必须返回 nil —— 语义是「不做重建」，logtail 会如实打日志，
//      而不是拿一个恒返回空的结果假装「重建完成、补齐 0 天」；
//   2. 有客户端时必须返回非 nil —— 即真的接了，不是永远走 nil 分支。
func TestHitsSourceAdapterNilClientDisablesRebuild(t *testing.T) {
	if got := hitsSourceAdapter(nil); got != nil {
		t.Fatalf("nil 客户端必须返回 nil HitsSource（语义是「不做重建」，要能如实上报），实际 %v", got)
	}
	// 非 nil 客户端：只要返回非 nil 即证明接线路径存在（真调用需要网络，不在此覆盖）。
	if got := hitsSourceAdapter(&vlsup.Client{}); got == nil {
		t.Fatal("有 VL 客户端时必须返回非 nil HitsSource，否则启动重建永远不生效")
	}
}
