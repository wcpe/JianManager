package ingest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是「锁超时 detail 带游标源」（2026-10-04 父级裁定 ①）的确定性红/绿：
//
// 现场：`config_lock_timeout`/`candidate_lock_timeout` 的快速放弃路径拿不到 m.mu，
// 若在超时点读游标又会阻塞 ⇒ detail 只剩错误码、**点名被掩蔽**（对账卡在哪无从得知）。
// 修法：持锁路径在"取候选/推进游标"时写入**无锁快照**（`reconcileCursorKeySnapshot`），
// 超时路径零依赖读快照 ⇒ detail 带上"已推进到哪"。
//
// 绿（现状）：第一轮正常取候选（快照=游标源）→ 第二轮锁被长持 ⇒ detail 含
// `config_lock_timeout@cursor=<源>` 且源非空；
// 红（变异：删去 `@cursor=` 后缀）⇒ 断言转红。
func TestReconcileLockTimeoutCarriesCursorSource(t *testing.T) {
	m := &Manager{
		pipes: map[string]*pipeline.Pipeline{
			"inst:a/file": nil, "inst:b/file": nil,
		},
		sources: map[string]SourceConfig{
			"inst:a/file": {LogSourceID: "inst:a", SourceGeneration: "g1"},
			"inst:b/file": {LogSourceID: "inst:b", SourceGeneration: "g1"},
		},
		vlNotReady: map[string]string{},
	}
	m.reconcileLoop = ReconcileLoopConfig{Enabled: true, RoundBudget: 30 * time.Second, MaxSourcesPerRound: 8}
	m.reconcileLockBudget = 200 * time.Millisecond

	// 第一轮：无锁竞争 ⇒ 取候选成功（顺带写入游标源快照）
	_ = m.ReconcileRoundNow(context.Background())
	require.NotEmpty(t, m.reconcileCursorKeyOfSnapshot(), "第一轮取候选后快照必须已写入")

	// 第二轮：锁被长持 ⇒ 走快速放弃路径 ⇒ detail 必须带 @cursor
	m.mu.Lock()
	go func() { time.Sleep(600 * time.Millisecond); m.mu.Unlock() }()
	res := m.ReconcileRoundNow(context.Background())
	detail := strings.Join(res.Errors, " | ")
	require.Contains(t, detail, "config_lock_timeout@cursor=",
		"锁超时 detail 必须带游标源（红证=删 @cursor 后缀 ⇒ 点名掩蔽）")
	require.NotEqual(t, "config_lock_timeout@cursor=", detail, "游标源不得为空（首轮已存快照）")
}

// 变异记录（2026-10-04 实测，逐字还原）：
//   - 变异：删去两处超时 detail 的 `@cursor=` 后缀 ⇒ 本用例第一断言转红；还原后转绿。
