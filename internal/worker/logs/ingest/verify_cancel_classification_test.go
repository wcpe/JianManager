package ingest

// 复审 P1-3 回归：校验分簇并发时，cancel() 只能由**确定性**根因触发，
// 「本簇窗口到期仍不可见」这类非确定性结论既不得取消其余簇，也不得覆盖真根因。
//
// 现场（旧实现）：verifyErrorIsSemantic 的名单里含 `not fully visible before deadline`——
// 而它正是本簇超时时**唯一**的错误文案。于是任一簇到期就 `cancel()` 其余簇，
// 尚未派发/正在查询的簇因此拿不到它本该读到的真根因（内容不一致），
// 最终 verdict 退化成「到期仍不可见」；现场只能靠反复复现才看得出根因。
//
// 本文件的用例：
//  1. TestVerifyProjectionTimeoutChunkDoesNotMaskContentMismatch —— 一簇超时 + 另一簇稍后
//     读到内容不一致 → 最终 verdict 必须是**内容不一致**（把超时文案放回 semantic 名单即红）。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// verifySplitFakeVL 是「静默窗 + 延迟返回」的假 VL，用来构造
// 「多簇到期仍不可见、另一簇稍后读到内容不一致」这一并发形态。
//
// 静默窗（silentBefore 之前的查询）一律返回空结果：命中它的簇只能退避重试到自己的窗口到期，
// 以 `not fully visible before deadline` 收场；其余查询延迟 delay 后再返回，其中 victim
// 的行内容被改写（内容不一致），从而保证真根因**晚于**超时簇到达——这正是旧实现里被
// `cancel()` 覆盖掉的那一刻。
type verifySplitFakeVL struct {
	events       []logtypes.Event
	silentBefore time.Time
	delay        time.Duration
	victim       string

	mu            sync.Mutex
	silentQueries int
	servedQueries int
}

func (f *verifySplitFakeVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start, errStart := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
	end, errEnd := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
	if errStart != nil || errEnd != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// 查询窗口完全落在静默窗内 → 返回空结果（模拟「这一刻数据还不可见」）。
	if end.Before(f.silentBefore) {
		f.mu.Lock()
		f.silentQueries++
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.servedQueries++
	f.mu.Unlock()
	for _, e := range f.events {
		when, err := time.Parse(time.RFC3339Nano, e.EventTimeUTC)
		if err != nil || when.Before(start) || when.After(end) {
			continue
		}
		msg := e.Message
		if e.EventID == f.victim {
			msg = "tampered content"
		}
		fmt.Fprintf(w, "{\"event_id\":%q,\"canonical_content_hash\":%q,\"_time\":%q,\"_msg\":%q,\"level\":%q,\"stream\":%q}\n",
			e.EventID, e.CanonicalHash, e.EventTimeUTC, msg, e.Level, e.Stream)
	}
}

func (f *verifySplitFakeVL) counts() (silent, served int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.silentQueries, f.servedQueries
}

// TestVerifyProjectionTimeoutChunkDoesNotMaskContentMismatch 是复审 P1-3 的转红闸门：
// 一簇窗口到期（不可见）+ 另一簇稍后读到内容不一致 → 最终 verdict 必须是内容不一致。
//
// 时序编排（确定性，不依赖机器快慢）：
//   - 三簇：前两簇落在静默窗（各自退避重试到自己的窗口到期），第三簇含被改写的行；
//   - 并发度 2 → 前两簇先各占一个 worker，第三簇只能等其中一个收场（≈窗口到期）后才被派发；
//   - 第三簇的查询在夹具里延迟一会，故它的内容不一致必然**晚于**超时簇到达。
//
// 旧实现下：第一个超时簇的判定类错误触发 cancel() → 第三簇要么根本不被派发（派发循环见
// ctx.Err() 即 break），要么查询被取消 → 最终 verdict 退化成「到期仍不可见」→ 本用例转红。
func TestVerifyProjectionTimeoutChunkDoesNotMaskContentMismatch(t *testing.T) {
	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	events := make([]logtypes.Event, 0, 300)
	add := func(hour int, label string) {
		for i := 0; i < 100; i++ {
			when := base.Add(time.Duration(hour)*time.Hour + time.Duration(i)*time.Millisecond)
			events = append(events, logtypes.BuildEvent(
				logtypes.SourceIdentity{LogSourceID: "inst:cancel/file", SourceGeneration: "g1", ParserVersion: "v1"},
				logtypes.RecordRange{Start: uint64(hour*1000 + i), End: uint64(hour*1000 + i)},
				when.Format(time.RFC3339Nano), when.Format(time.RFC3339Nano),
				"INFO", "stdout", fmt.Sprintf("%s %03d", label, i),
			))
		}
	}
	add(0, "silent-a")
	add(1, "silent-b")
	add(2, "late")
	victim := events[len(events)-1].EventID
	fixture := &verifySplitFakeVL{
		events:       events,
		silentBefore: base.Add(90 * time.Minute), // hour 0/1 静默，hour 2 正常返回
		delay:        150 * time.Millisecond,     // 真根因晚于超时簇到达
		victim:       victim,
	}
	srv := httptest.NewServer(fixture)
	t.Cleanup(srv.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	m := newVerifyTestManager(10*time.Millisecond, 20*time.Millisecond, time.Second)
	m.verifyChunkEvents = 100                      // 三簇
	m.verifyChunkConcurrency = 2                   // 前两簇各占一个 worker，第三簇排队
	m.verificationTimeout = 300 * time.Millisecond // 静默簇的窗口

	err = m.verifyProjection(client, SourceConfig{LogSourceID: "inst:cancel/file", SourceGeneration: "g1"},
		"projection-1", events)
	require.Error(t, err, "该批在 VL 中并不完整，校验必须失败")
	silent, served := fixture.counts()
	require.GreaterOrEqual(t, silent, 2, "静默簇必须真的走到「退避重试后到期」这条路径（实测 %d 次查询）", silent)
	require.GreaterOrEqual(t, served, 1, "内容不一致簇必须真的读到被改写的行（实测 %d 次查询）", served)
	require.Contains(t, err.Error(), "projection content mismatch for event_id "+victim,
		"真根因（内容不一致）必须胜出：超时簇不得取消其余簇、更不得覆盖根因（实测 err=%v）", err)
	require.NotContains(t, err.Error(), "not fully visible before deadline",
		"最终 verdict 不得退化成「到期仍不可见」（那就是超时簇覆盖了真根因）")
	require.NotContains(t, err.Error(), "aborted by batch cancellation",
		"不得出现批量取消噪音：超时不是确定性根因，不该触发取消")
}
