package ingest

// 查询窗口越界的复现回归（2026-10-28 生产故障）。
//
// ## 现场
//
// 19 个源里 10 个停采，`reclaim_pos=0` 从进程启动到当天 17:54 一次没动过。
// 逐条读数：`backlog=20165/20000`（撞 WAL 上限 ⇒ 暂停采集），
// `delivery_pos=0`，`unresolved=3 blocking=0`（缺口本身不阻塞）。
//
// ## 根因链（每一环都用 curl 单点验证过）
//
//	1. 账本头部有 ~178 万字节待投递，含 2026-09-20/21 的历史归档补数据。
//	2. 投递先写 VL，再做「投影校验」回查确认。
//	3. 校验查询的窗口按事件时间算 ⇒ start=2026-09-20T14:19:49Z。
//	4. VL hot 起在 `-search.maxQueryTimeRange=744h`（= retention 30d + 24h，
//	   见 vlsup.(InstanceConfig).QueryTimeRangeLimitChecked）。
//	5. 于是查询拿到 **HTTP 400**：
//	     `too big time range selected: [...]; it cannot exceed -search.maxQueryTimeRange=744h0m0s`
//	6. verifyProjection 主循环把这个 400 归到「可重试」一类
//	   （它不在 verifyErrorIsSemantic 的判定名单里）。
//	7. 重试到窗口到期 ⇒ 最终文案变成
//	   `not fully visible before deadline` —— **把「查询被拒绝」说成了「数据没可见」**。
//
// ## 为什么必须区分这两件事
//
//   - 「数据还没可见」是**暂时**的：等一会儿索引建好就会出现，重试是对的。
//   - 「查询被拒绝（窗口越界）」是**永久**的：窗口不会随时间变小（数据只会更旧），
//     重试一万次也还是 400。而它伪装成「暂时」的代价是整个源被钉死：
//     delivery 不动 ⇒ reclaim 不动 ⇒ WAL 删不掉 ⇒ 采集暂停。
//
// 这两个结论在运维上要求**完全相反**的动作，所以不能共用一条错误文案。
//
// ## 本文件钉住的性质
//
//  1. 窗口越界的 400（VL 的真实文案）必须被识别为**永久失败**，不得重试到窗口到期，
//     且错误文案不得冒充 `not fully visible before deadline`。
//  2. 真正的「尚未可见」（200 + 空响应）仍必须可重试——不能被本修复误伤。
//  3. 非窗口越界的 4xx 保持既有语义（立即失败），不因本修复而改变。

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// vlTimeRangeOverflowBody 是 VictoriaLogs 在时间窗超出 -search.maxQueryTimeRange 时
// 返回的**原文**（2026-10-28 在生产实例 19461 上 curl 实测得到，逐字照抄）。
//
// 为什么照抄原文而不是自己编一句：本修复的判据必须能认出**真实服务端的措辞**。
// 用一句「更像报错」的自造文案会让测试通过而生产照旧。
const vlTimeRangeOverflowBody = "too big time range selected: [2026-09-20T00:00:00Z, 2026-10-27T23:59:59.999999999Z]; " +
	"it cannot exceed -search.maxQueryTimeRange=744h0m0s; " +
	"see https://docs.victoriametrics.com/victorialogs/querying/#resource-usage-limits"

// TestVerifyProjectionFailsFastOnTimeRangeOverflow 是本次生产故障的直接复现。
//
// 修复前：请求 1 次拿到 400 ⇒ 归为可重试 ⇒ 退避重试到 5s 窗口到期 ⇒
// 返回 `not fully visible before deadline`（把「查询被拒」说成「数据不可见」）。
// 修复后：请求 1 次 ⇒ 立即返回，文案指明是时间窗越界。
func TestVerifyProjectionFailsFastOnTimeRangeOverflow(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(vlTimeRangeOverflowBody))
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, err)

	// 退避 10ms→50ms，窗口 5s：若按可重试处理，会退避重试约 10 次以上。
	m := newVerifyTestManager(10*time.Millisecond, 50*time.Millisecond, 5*time.Second)
	start := time.Now()
	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:1/file", SourceGeneration: "g1"},
		"projection-1", []logtypes.Event{verifyTestEvent()})
	elapsed := time.Since(start)

	require.Error(t, err, "窗口越界必须失败（不能当作校验通过）")
	require.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"窗口越界是永久结论，不得重试：应恰好 1 次查询")
	require.Less(t, elapsed, time.Second,
		"必须立即返回，而不是等满校验窗口（实测 %s）", elapsed)

	// 最关键的一条：文案不得冒充「数据还没可见」。
	// 那会把运维引向「等一等就好了」，而实际上等多久都不会好。
	require.NotContains(t, err.Error(), "not fully visible before deadline",
		"窗口越界不得被表述为「数据未可见」——处置动作完全相反")
}

// TestVerifyProjectionStillRetriesWhenDataNotVisible 是上一条的**反向对照**。
//
// 真正的可见性延迟（200 + 空响应）必须仍然可重试，否则本修复会把
// 「VL 索引慢」这种正常情形误判为永久失败。
func TestVerifyProjectionStillRetriesWhenDataNotVisible(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK) // 200 + 空 NDJSON = 尚未可见
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, err)

	m := newVerifyTestManager(10*time.Millisecond, 50*time.Millisecond, 300*time.Millisecond)
	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:1/file", SourceGeneration: "g1"},
		"projection-1", []logtypes.Event{verifyTestEvent()})

	require.Error(t, err)
	require.Greater(t, atomic.LoadInt32(&calls), int32(1),
		"可见性延迟必须重试（这不是永久失败）")
	require.Contains(t, err.Error(), "not fully visible before deadline",
		"这类确实该报「未可见」")
}

// TestVerifyProjectionGenericBadRequestStaysPermanent 是第三条对照：
// 非窗口越界的 4xx 仍走既有的「永久失败」路径，行为不因本修复而改变。
func TestVerifyProjectionGenericBadRequestStaysPermanent(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("cannot execute query [bad range]"))
	}))
	defer srv.Close()

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, err)

	m := newVerifyTestManager(10*time.Millisecond, 50*time.Millisecond, 5*time.Second)
	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:1/file", SourceGeneration: "g1"},
		"projection-1", []logtypes.Event{verifyTestEvent()})

	require.Error(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "普通 4xx 不得重试")
	require.Contains(t, err.Error(), "verification failed permanently",
		"普通 4xx 的既有文案不得改变：%v", err)
}
