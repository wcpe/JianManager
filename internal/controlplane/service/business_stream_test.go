package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// 本文件覆盖 FR-522 订阅过滤 + FR-536 连接治理（有界队列 / 慢消费者断开 / 单 Token 上限）。

// mkEvent 造一条探针业务事件（RawJson 为探针上报的原始帧，含 topic 与 data）。
func mkEvent(instanceUUID, domain, topic, dedupKey string) *workerpb.PluginEvent {
	raw, _ := json.Marshal(map[string]any{
		"type":      "event",
		"event":     "activity_progress",
		"domain":    domain,
		"dedupKey":  dedupKey,
		"topic":     topic,
		"data":      map[string]any{"player": "steve", "delta": 10},
		"timestamp": 1730000000,
	})
	return &workerpb.PluginEvent{
		InstanceUuid: instanceUUID,
		Type:         "activity_progress",
		Timestamp:    1730000000,
		Domain:       domain,
		DedupKey:     dedupKey,
		RawJson:      string(raw),
	}
}

// drain 取空订阅者队列（非阻塞）。
func drain(sub *BusinessSubscriber) []*BusinessFrame {
	var out []*BusinessFrame
	for {
		select {
		case f := <-sub.Frames():
			out = append(out, f)
		default:
			return out
		}
	}
}

// TestParseBusinessStreamFilter_Match 覆盖 domains 精确匹配 + topics 后缀通配（§3.1）。
func TestParseBusinessStreamFilter_Match(t *testing.T) {
	cases := []struct {
		name    string
		domains string
		topics  string
		frame   *BusinessFrame
		want    bool
	}{
		{"无过滤全通", "", "", &BusinessFrame{Domain: "activity", Topic: "activity.x"}, true},
		{"域名命中", "activity,economy", "", &BusinessFrame{Domain: "activity"}, true},
		{"域名未命中", "economy", "", &BusinessFrame{Domain: "activity"}, false},
		{"域名大小写不敏感", "Activity", "", &BusinessFrame{Domain: "activity"}, true},
		{"主题精确命中", "", "activity.spring-2026", &BusinessFrame{Topic: "activity.spring-2026"}, true},
		{"主题精确未命中", "", "activity.spring-2026", &BusinessFrame{Topic: "activity.winter-2026"}, false},
		{"主题后缀通配命中", "", "activity.*", &BusinessFrame{Topic: "activity.spring-2026"}, true},
		{"主题后缀通配未命中", "", "economy.*", &BusinessFrame{Topic: "activity.spring-2026"}, false},
		{"多主题任一命中", "", "economy.*,activity.*", &BusinessFrame{Topic: "activity.a"}, true},
		{"域名与主题取交集-命中", "activity", "activity.*", &BusinessFrame{Domain: "activity", Topic: "activity.a"}, true},
		{"域名与主题取交集-主题不命中", "activity", "economy.*", &BusinessFrame{Domain: "activity", Topic: "activity.a"}, false},
		{"空白项被忽略", " , activity , ", "", &BusinessFrame{Domain: "activity"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseBusinessStreamFilter(tc.domains, tc.topics)
			if got := f.Match(tc.frame); got != tc.want {
				t.Fatalf("Match=%v，期望 %v（domains=%q topics=%q frame=%+v）", got, tc.want, tc.domains, tc.topics, tc.frame)
			}
		})
	}
}

// TestBusinessStreamService_IngestFiltersByDomain 未匹配 domain 的订阅者收不到事件（§8 验收 2）。
func TestBusinessStreamService_IngestFiltersByDomain(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(100, time.Minute))
	act, err := svc.Subscribe("agent:1", ParseBusinessStreamFilter("activity", ""))
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	eco, err := svc.Subscribe("agent:2", ParseBusinessStreamFilter("economy", ""))
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}

	svc.Ingest("node-1", mkEvent("inst-1", "activity", "activity.a", "e1"))
	svc.Ingest("node-1", mkEvent("inst-1", "economy", "economy.b", "e2"))

	if got := drain(act); len(got) != 1 || got[0].Domain != "activity" {
		t.Fatalf("activity 订阅者应只收到 1 条 activity 事件，实得 %d 条", len(got))
	}
	if got := drain(eco); len(got) != 1 || got[0].Domain != "economy" {
		t.Fatalf("economy 订阅者应只收到 1 条 economy 事件，实得 %d 条", len(got))
	}
	// 非业务事件（domain 为空）不进入本通道。
	svc.Ingest("node-1", &workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "player_join"})
	if got := drain(act); len(got) != 0 {
		t.Fatalf("domain 为空的监控事件不应进入业务通道，实得 %d 条", len(got))
	}
}

// TestBusinessStreamService_IngestFiltersByTopic 主题通配过滤（§8 验收 1/2）。
func TestBusinessStreamService_IngestFiltersByTopic(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(100, time.Minute))
	sub, err := svc.Subscribe("agent:1", ParseBusinessStreamFilter("", "activity.spring-*"))
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	svc.Ingest("node-1", mkEvent("inst-1", "activity", "activity.spring-2026", "e1"))
	svc.Ingest("node-1", mkEvent("inst-1", "activity", "activity.winter-2026", "e2"))

	got := drain(sub)
	if len(got) != 1 || got[0].Topic != "activity.spring-2026" {
		t.Fatalf("应只收到 1 条 spring 主题，实得 %d 条", len(got))
	}
}

// TestBusinessStreamService_IngestEnvelopeAndPayload 信封字段补齐 + 载荷原样透传。
func TestBusinessStreamService_IngestEnvelopeAndPayload(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(100, time.Minute))
	sub, _ := svc.Subscribe("agent:1", BusinessStreamFilter{})

	svc.Ingest("node-main", mkEvent("inst-uuid", "activity", "activity.spring-2026", "e-8891"))
	got := drain(sub)
	if len(got) != 1 {
		t.Fatalf("应收到 1 条事件，实得 %d", len(got))
	}
	f := got[0]
	if f.Node != "node-main" || f.Domain != "activity" || f.Topic != "activity.spring-2026" || f.DedupKey != "e-8891" {
		t.Fatalf("信封字段不符: %+v", f)
	}
	// 载荷是探针帧里的 data 原样透传（未解析、未改写）。
	var payload map[string]any
	if err := json.Unmarshal(f.Payload, &payload); err != nil {
		t.Fatalf("载荷不是合法 JSON: %v（%s）", err, string(f.Payload))
	}
	if payload["player"] != "steve" {
		t.Fatalf("载荷内容不符: %v", payload)
	}
	// payload 内容不得进入指标/日志字段（这里只断言它没有出现在信封的其它可见字段里）。
	if strings.Contains(f.InstanceName, "steve") {
		t.Fatal("载荷内容泄漏进信封元数据")
	}
}

// TestBusinessStreamService_SlowConsumerDisconnected 慢消费者被断开，其他订阅者不受影响（FR-536 §3 / §8 验收 1）。
func TestBusinessStreamService_SlowConsumerDisconnected(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(10_000, time.Minute))
	// 故意不消费的订阅者。
	slowSub, _ := svc.Subscribe("agent:slow", BusinessStreamFilter{})
	// 正常消费者：每来一条即取走。
	fastSub, _ := svc.Subscribe("agent:fast", BusinessStreamFilter{})

	received := 0
	const total = BusinessSubscriberQueueSize + 50
	for i := 0; i < total; i++ {
		svc.Ingest("node-1", mkEvent("inst-1", "activity", "activity.a", "e"))

		// 取走正常订阅者的事件，模拟消费跟得上。
		select {
		case <-fastSub.Frames():
			received++
		default:
		}
		// 慢订阅者停止信号一旦触发，说明在队列满那一刻被断开。
	}

	select {
	case <-slowSub.Stop():
	default:
		t.Fatal("慢消费者队列打满后应收到停止信号")
	}
	if !slowSub.SlowConsumer() {
		t.Fatal("慢消费者应被标记为 slow 以便收尾时发送 SLOW_CONSUMER 错误帧")
	}
	select {
	case <-fastSub.Stop():
		t.Fatal("正常订阅者不应被牵连断开")
	default:
	}
	if received < total-2 {
		t.Fatalf("正常订阅者应收到几乎全部事件，实得 %d/%d", received, total)
	}
	if m := svc.Metrics(); m.SlowConsumerDisconnects != 1 {
		t.Fatalf("慢消费断开计数应为 1，实得 %d", m.SlowConsumerDisconnects)
	}
	// 慢消费者被移除后，其 Token 配额应归还（否则重连会被自己的旧连接挡住）。
	if n := svc.TokenConnectionCount("agent:slow"); n != 0 {
		t.Fatalf("慢消费者断开后应归还配额，实得 %d", n)
	}
}

// TestBusinessStreamService_SubscribeRespectsTokenLimit 单 Token 并发上限（FR-536 §4 / §8 验收 4）。
func TestBusinessStreamService_SubscribeRespectsTokenLimit(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(10, time.Minute))
	for i := 0; i < BusinessStreamMaxPerToken; i++ {
		if _, err := svc.Subscribe("agent:1", BusinessStreamFilter{}); err != nil {
			t.Fatalf("第 %d 个连接不应被拒: %v", i+1, err)
		}
	}
	if _, err := svc.Subscribe("agent:1", BusinessStreamFilter{}); err == nil {
		t.Fatal("超出单 Token 上限应返回错误（路由据此外 429）")
	} else if err != ErrBusinessStreamTooManyConnections {
		t.Fatalf("错误类型不符: %v", err)
	}
	// 其他 Token 不受影响。
	if _, err := svc.Subscribe("agent:2", BusinessStreamFilter{}); err != nil {
		t.Fatalf("其他 Token 不应被限流: %v", err)
	}
	// 注销后配额归还。
	sub, _ := svc.Subscribe("agent:3", BusinessStreamFilter{})
	svc.Unsubscribe(sub)
	if n := svc.TokenConnectionCount("agent:3"); n != 0 {
		t.Fatalf("注销后配额应为 0，实得 %d", n)
	}
}

// TestBusinessStreamService_GlobalLimit 全局并发上限同样生效（保护 CP 内存）。
func TestBusinessStreamService_GlobalLimit(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(10, time.Minute))
	svc.maxGlobal = 2
	svc.maxPerTok = 100
	for i := 0; i < 2; i++ {
		if _, err := svc.Subscribe("agent:"+fmtInt64(int64(i)), BusinessStreamFilter{}); err != nil {
			t.Fatalf("第 %d 个连接不应被拒: %v", i+1, err)
		}
	}
	if _, err := svc.Subscribe("agent:9", BusinessStreamFilter{}); err == nil {
		t.Fatal("超出全局上限应返回错误")
	}
}

// TestBusinessStreamService_MetricsSnapshot 通道维度指标可查（§6）。
func TestBusinessStreamService_MetricsSnapshot(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(3, time.Minute))
	sub, _ := svc.Subscribe("agent:1", BusinessStreamFilter{})
	for i := 0; i < 5; i++ {
		svc.Ingest("node-1", mkEvent("inst-1", "activity", "activity.a", "e"))
	}
	m := svc.Metrics()
	if m.Connections != 1 {
		t.Fatalf("连接数应为 1，实得 %d", m.Connections)
	}
	if m.EventsReceived != 5 {
		t.Fatalf("接收计数应为 5，实得 %d", m.EventsReceived)
	}
	if m.EventsDropped != 2 {
		t.Fatalf("容量 3 写入 5 条应丢弃 2 条，实得 %d", m.EventsDropped)
	}
	if m.BufferSize != 3 || m.BufferOldestSeq != 3 || m.BufferNewestSeq != 5 {
		t.Fatalf("缓冲边界不符: size=%d oldest=%d newest=%d", m.BufferSize, m.BufferOldestSeq, m.BufferNewestSeq)
	}
	_ = sub
}

// TestBusinessStreamService_SubscribeStopsOnStop 服务侧停止信号能让写协程退出（防泄漏）。
func TestBusinessStreamService_SubscribeStopsOnStop(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(10, time.Minute))
	sub, _ := svc.Subscribe("agent:1", BusinessStreamFilter{})
	svc.Unsubscribe(sub)
	select {
	case <-sub.Stop():
	default:
		t.Fatal("注销后 Stop 信号应已关闭")
	}
	// 重复注销必须幂等（SSE 处理器 defer 与慢消费者路径可能都调）。
	svc.Unsubscribe(sub)
}

// TestSplitRawEnvelope_ExtractsAction action 从 data.action 提升到信封。
//
// 回归背景（四仓联调）：Loom 的 BusinessEvent 需要 action 区分同域内的不同事件类型，
// 而探针的 BusinessEventSink 签名没有 action 形参——插件把它放在 fields 里，
// 最终落在帧的 data.action。JM 只做「提升」，不理解其语义。
func TestSplitRawEnvelope_ExtractsAction(t *testing.T) {
	t.Run("data.action 被提取", func(t *testing.T) {
		raw := `{"topic":"economy.x","seq":7,"data":{"action":"deposit","amount":"100"}}`
		env := splitRawEnvelope(raw)
		assert.Equal(t, "deposit", env.Action)
		assert.Equal(t, int64(7), env.Seq)
		assert.Equal(t, "economy.x", env.Topic)
	})

	t.Run("顶层 action 也能取（兼容将来探针上提）", func(t *testing.T) {
		raw := `{"topic":"t","seq":1,"action":"mail_sent","data":{}}`
		assert.Equal(t, "mail_sent", splitRawEnvelope(raw).Action)
	})

	t.Run("没有 action 时为空（不报错、不影响其他字段）", func(t *testing.T) {
		raw := `{"topic":"t","seq":2,"data":{"foo":"bar"}}`
		env := splitRawEnvelope(raw)
		assert.Equal(t, "", env.Action)
		assert.Equal(t, int64(2), env.Seq)
	})

	t.Run("坏 JSON 整体降级为空信封", func(t *testing.T) {
		env := splitRawEnvelope("{not json")
		assert.Equal(t, "", env.Action)
		assert.Equal(t, int64(0), env.Seq)
	})

	t.Run("data 不是对象时不 panic", func(t *testing.T) {
		env := splitRawEnvelope(`{"topic":"t","seq":3,"data":"plain string"}`)
		assert.Equal(t, "", env.Action)
		assert.Equal(t, int64(3), env.Seq)
	})
}
