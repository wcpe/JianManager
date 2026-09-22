package service

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// webhookProbe 一个可观测的 webhook 端点：hits 计命中次数；block 非 nil 时每个请求
// 都会挂在该 channel 上直到它被放行（用于把「投递是否发生在调用线程上」变成可测量的时间差）。
type webhookProbe struct {
	srv   *httptest.Server
	block chan struct{}
	once  sync.Once
	hits  int64
}

func newWebhookProbe(t *testing.T, block bool) *webhookProbe {
	t.Helper()
	p := &webhookProbe{}
	if block {
		p.block = make(chan struct{})
	}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&p.hits, 1)
		if p.block != nil {
			<-p.block
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		p.Release()
		p.srv.Close()
	})
	return p
}

// Release 放行所有挂起的请求。幂等（sync.Once）——用例体与 t.Cleanup 都会调用它，
// 直接 close 会 double-close panic。
func (p *webhookProbe) Release() {
	if p.block == nil {
		return
	}
	p.once.Do(func() { close(p.block) })
}

func (p *webhookProbe) Hits() int64 { return atomic.LoadInt64(&p.hits) }

// probeAlertDB 造「库 + 指向 probe 的启用 webhook 通道 + 一条启用 metric 规则」并返回规则。
// 规则显式带 ChannelIDs，故 Fire 会真的走到外发投递（而非因无通道而空转）。
func probeAlertDB(t *testing.T, envKey, targetType string, probe *webhookProbe) (*gorm.DB, *model.AlertRule) {
	t.Helper()
	db := newAlertTestDB(t)
	t.Setenv(envKey, probe.srv.URL)

	on := true
	ch, err := NewAlertChannelService(db).Create(ChannelRequest{
		Name: "probe-wh", Type: model.ChannelTypeWebhook, Enabled: &on,
		Config: ChannelConfig{URL: "${" + envKey + "}"},
	})
	require.NoError(t, err)

	rule := &model.AlertRule{
		Name: "容量趋势", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		TargetType: targetType, Enabled: true,
	}
	rule.ChannelIDs = "[" + itoa(ch.ID) + "]"
	require.NoError(t, db.Create(rule).Error)
	return db, rule
}

// trendResults 造一条命中阈值（下界 3 天 < 阈值 7 天）的预测。
func trendResults() []ForecastResult {
	low := 3.0
	return []ForecastResult{{
		TargetID: "node-x", MetricKey: model.MetricNodeDiskUsed,
		NowValue: 90, LimitValue: 100, Confidence: ForecastConfidenceHigh, ExhaustLowDays: &low,
	}}
}

// TestCapacityTrendAlerter_NotifyDoesNotBlockOnWebhook （M-8）趋势告警不得在
// `GET /metrics/capacity/forecast` 的调用线程上同步等待外部通道。
//
// 缺陷：`Notify` 由处理器**同步**调用，内部 `dispatcher.Fire` 会同步 `d.notify(...)`
// 对每个通道**同步 POST**（`ChannelNotifier.client` 超时 **10s**，含 Telegram/邮件路径）。
// 于是该 GET 的最坏耗时可被外部 webhook 拖住 10s 并全程阻塞 gin handler，
// 而前端 60s 轮询会持续占用连接。这是「用户请求驱动的写 + 外发」，
// 与既有 `AlertEvaluator` 的后台轮询形态不同。
//
// 断言方式：通道指向一个**不响应**的 webhook（由用例放行）。修复前 Notify 会挂在那里
// 直到 10s 超时 → 下面的 2s 预算内拿不到返回；修复后只做落库 + 入队，立即返回。
// 同时确认通知**最终仍会送达**——阻塞问题不能靠丢弃通知来解决。
func TestCapacityTrendAlerter_NotifyDoesNotBlockOnWebhook(t *testing.T) {
	probe := newWebhookProbe(t, true)
	db, _ := probeAlertDB(t, "JM_TEST_M8_SLOW", string(model.MetricScopeNode), probe)
	dispatcher := NewAlertDispatcher(db)
	defer dispatcher.Stop()
	dispatcher.now = time.Now
	alerter := NewCapacityTrendAlerter(db, dispatcher)

	done := make(chan int, 1)
	go func() { done <- alerter.Notify(model.MetricScopeNode, 7, "n1", trendResults(), 7) }()
	select {
	case fired := <-done:
		require.Equal(t, 1, fired, "阈值命中应触发 1 条")
	case <-time.After(2 * time.Second):
		t.Fatalf("Notify 阻塞超过 2s：外发投递仍在 GET 请求线程上同步执行（notifier 超时为 10s）")
	}

	// 同步部分（去抖判定 + 事件落库）必须已经完成。
	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1, "事件落库仍应同步完成")

	// 异步投递必须最终送达。
	probe.Release()
	require.Eventually(t, func() bool { return probe.Hits() >= 1 }, 5*time.Second, 20*time.Millisecond,
		"异步投递仍须最终送达外部通道")

	// 去抖仍生效：60s 轮询复查不产生第二条事件、也不再投递。
	require.Equal(t, 0, alerter.Notify(model.MetricScopeNode, 7, "n1", trendResults(), 7))
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1, "重发间隔内不得新建事件")
	require.Equal(t, int64(1), probe.Hits(), "重发间隔内不得重复外发")
}

// TestCapacityTrendAlerter_ConcurrentNotifyExactlyOnce （M-8 并发安全）
// 并发查询下同一去抖键只触发一次、只外发一次。
//
// 风险场景：`Notify` 由 HTTP 处理器并发调用（前端 60s 轮询 × 多标签页/多调用方），
// 而 `dueForNotify`（读上一事件）与 `dispatcher.Fire`（写新事件）之间存在窗口。
// 若只把投递异步化而不串行化这对读-写，两个并发查询会**都**读到「无历史事件」、
// 都判定该发 → 落两条事件 + 外发两次。故本用例是 M-8 的核心约束。
func TestCapacityTrendAlerter_ConcurrentNotifyExactlyOnce(t *testing.T) {
	probe := newWebhookProbe(t, false)
	db, _ := probeAlertDB(t, "JM_TEST_M8_ONCE", string(model.MetricScopeNode), probe)
	dispatcher := NewAlertDispatcher(db)
	defer dispatcher.Stop()
	dispatcher.now = time.Now
	alerter := NewCapacityTrendAlerter(db, dispatcher)

	const callers = 8
	var wg sync.WaitGroup
	var total int64
	start := make(chan struct{}) // 让所有 goroutine 尽量同时进入 Notify
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			atomic.AddInt64(&total, int64(alerter.Notify(model.MetricScopeNode, 7, "n1", trendResults(), 7)))
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, int64(1), atomic.LoadInt64(&total),
		"并发查询下只应有 1 次触发（去抖读-写对必须串行）")
	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1, "去抖窗口内只落一条事件")

	require.Eventually(t, func() bool { return probe.Hits() >= 1 }, 5*time.Second, 20*time.Millisecond,
		"至少投递一次")
	// 留出可观测窗口后再确认没有第二次外发。
	require.Never(t, func() bool { return probe.Hits() > 1 }, 500*time.Millisecond, 25*time.Millisecond,
		"同一条告警不得重复外发")
}

// TestAlertDispatcher_DeliverAsyncReachesChannel （M-8 单元）`DeliverAsync=true` 时
// 外发投递移到后台队列，`Fire` 立即返回，但通知最终仍被通道收到。
// 直接打 `Fire` 以隔离 `CapacityTrendAlerter` 的去抖逻辑，验证队列本身的行为。
func TestAlertDispatcher_DeliverAsyncReachesChannel(t *testing.T) {
	probe := newWebhookProbe(t, true)
	db, rule := probeAlertDB(t, "JM_TEST_M8_FIRE", string(model.MetricScopeNode), probe)
	dispatcher := NewAlertDispatcher(db)
	defer dispatcher.Stop()
	dispatcher.now = time.Now

	done := make(chan struct{})
	go func() {
		dispatcher.Fire(AlertTrigger{
			Rule: rule, TargetID: 7, DedupKey: "k-async", Message: "m",
			Resolvable: false, DeliverAsync: true,
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("DeliverAsync=true 时 Fire 不得等待外部通道")
	}

	probe.Release()
	require.Eventually(t, func() bool { return probe.Hits() >= 1 }, 5*time.Second, 20*time.Millisecond,
		"异步投递必须最终送达")
}

// TestAlertDispatcher_DeliverAsyncStopDrainsQueue （M-8 关停语义）`Stop()` 必须把队列里
// 在途的通知处理完再返回，不能让「已落库但没外发」静默发生。
func TestAlertDispatcher_DeliverAsyncStopDrainsQueue(t *testing.T) {
	probe := newWebhookProbe(t, false) // 不阻塞：本用例只验证「排空」
	db, rule := probeAlertDB(t, "JM_TEST_M8_DRAIN", string(model.MetricScopeNode), probe)
	dispatcher := NewAlertDispatcher(db)
	dispatcher.now = time.Now

	const queued = 5
	for i := 0; i < queued; i++ {
		dispatcher.enqueueNotify(rule, AlertNotification{
			Event: "alert_fired", RuleID: rule.UUID, Title: rule.Name, Message: "m",
		})
	}

	done := make(chan struct{})
	go func() { dispatcher.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Stop 未在合理时间内返回（worker 未退出或在途投递卡住）")
	}
	require.Equal(t, int64(queued), probe.Hits(), "Stop 返回时队列中的通知必须已全部投递")

	// 幂等：重复 Stop 不得 panic。
	require.NotPanics(t, func() { dispatcher.Stop() })
	// Stop 之后再入队不得 panic，且退化路径仍会把通知送达（内联投递）。
	require.NotPanics(t, func() {
		dispatcher.enqueueNotify(rule, AlertNotification{
			Event: "alert_fired", RuleID: rule.UUID, Title: rule.Name, Message: "after-stop",
		})
	})
	require.Equal(t, int64(queued+1), probe.Hits(), "关停后的入队退化为内联投递，不丢通知")
}

// TestAlertDispatcher_DeliverAsyncWithoutChannelsNoOp （M-8 边界）无匹配通道的规则
// （既无 ChannelIDs 也无 webhook 回退）走异步路径时不得 panic、也不产生外发，
// 但事件仍须落库——「投递无目标」与「没触发」必须可区分。
func TestAlertDispatcher_DeliverAsyncWithoutChannelsNoOp(t *testing.T) {
	db := newAlertTestDB(t)
	dispatcher := NewAlertDispatcher(db)
	defer dispatcher.Stop()
	dispatcher.now = time.Now

	rule := &model.AlertRule{
		Name: "无通道", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		TargetType: string(model.MetricScopeNode), Enabled: true, // 无 ChannelIDs、无 NotifyType
	}
	require.NoError(t, db.Create(rule).Error)

	require.NotPanics(t, func() {
		dispatcher.Fire(AlertTrigger{
			Rule: rule, TargetID: 3, DedupKey: "k-noch", Message: "m",
			Resolvable: false, DeliverAsync: true,
		})
	})
	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1, "无通道只影响外发，事件仍须落库")
}
