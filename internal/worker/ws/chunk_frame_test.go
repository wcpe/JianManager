package ws

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 分片帧的协议层用例（FR-526 §2.5）：验证 Worker 的 WS 读循环真的把
// `type=event`+`event=chunk` 的上行分片重组回原帧，并让它走原有的语义分发路径
// （command_result 投递给同步等待方 / 业务事件扇出）——即"下游完全无感"。

// splitIntoChunkFrames 按探针 FrameChunker 的形态把一帧原文本切成多片 chunk 帧。
//
// 与 ServerProbe 的 chunkJson 逐字对齐：字段**平铺在帧顶层**（不是塞进 data），
// 即 `{type,event,groupId,seq,total,ofType,data}`；`data` 是整帧 base64 的一段。
//
// 先按 wantChunks 估算片长（对齐到 4 的整数倍），再以**实际片数**回填 total——
// 对端也是"切完才算得出 total"，这里如实复刻。
func splitIntoChunkFrames(t *testing.T, groupID string, wantChunks int, raw []byte) []map[string]any {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(raw)
	per := (len(encoded) + wantChunks - 1) / wantChunks
	per -= per % 4 // 对齐到 4 的整数倍，保证每片可独立解码
	require.Positive(t, per, "片长必须为正")

	var parts []string
	for from := 0; from < len(encoded); from += per {
		to := from + per
		if to > len(encoded) {
			to = len(encoded)
		}
		parts = append(parts, encoded[from:to])
	}
	frames := make([]map[string]any, 0, len(parts))
	for seq, part := range parts {
		frames = append(frames, chunkFrame(groupID, seq, len(parts), part, "command_result"))
	}
	return frames
}

// chunkFrame 构造一帧顶层平铺的 chunk 帧（与探针 chunkJson 同形）。
func chunkFrame(groupID string, seq, total int, data, ofType string) map[string]any {
	return map[string]any{
		"type":    "event",
		"event":   "chunk",
		"groupId": groupID,
		"seq":     seq,
		"total":   total,
		"ofType":  ofType,
		"data":    data,
	}
}

// TestChunkFrame_CommandResultReassembled 分片的 command_result 到齐后投递给同步等待方。
func TestChunkFrame_CommandResultReassembled(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()

	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	// 探针侧：读到 command 后把回执**分片**发回（模拟超大 output 触发 FrameChunker）。
	go func() {
		var cmd map[string]any
		if err := conn.ReadJSON(&cmd); err != nil {
			return
		}
		reqID, _ := cmd["requestId"].(string)
		bigOutput := strings.Repeat("A", 2000) // 超阈值的大回执
		original, _ := json.Marshal(map[string]any{
			"type":  "event",
			"event": "command_result",
			"data": map[string]any{
				"requestId": reqID,
				"success":   true,
				"output":    bigOutput,
			},
		})
		for _, f := range splitIntoChunkFrames(t, "grp-1", 4, original) {
			if err := conn.WriteJSON(f); err != nil {
				return
			}
		}
	}()

	res, err := s.SendCommandAndWait("inst-1", "req-42", map[string]any{"type": "command", "requestId": "req-42"}, 5*time.Second)
	require.NoError(t, err, "分片回执必须能重组并投递给同步等待方")
	assert.True(t, res.Success)
	assert.Equal(t, 2000, len(res.Output), "重组后的 output 长度应与原帧一致")
	assert.Empty(t, res.Error)
}

// TestChunkFrame_BusinessEventReassembled 分片的业务事件到齐后按普通事件冒泡（下游无感）。
func TestChunkFrame_BusinessEventReassembled(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)

	var mu sync.Mutex
	var events []PluginEvent
	s.SetEventHandler(func(e PluginEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()

	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	// 原帧是一个带 domain/dedupKey 的业务事件。
	original := []byte(`{"type":"event","event":"activity_change","domain":"activity",` +
		`"dedupKey":"d-1","data":{"playerName":"alice","message":"` + strings.Repeat("B", 1500) + `"}}`)
	for _, f := range splitIntoChunkFrames(t, "grp-biz", 3, original) {
		require.NoError(t, conn.WriteJSON(f))
	}

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range events {
			if e.Type == "activity_change" {
				assert.Equal(t, "activity", e.Domain, "业务域须从重组出的原帧解析")
				assert.Equal(t, "d-1", e.DedupKey, "去重键须从重组出的原帧解析")
				assert.Equal(t, "alice", e.PlayerName)
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "重组后的业务事件应冒泡给订阅者")
}

// TestChunkFrame_IncompleteNotEmitted 缺帧时不冒泡任何事件（不产生半成品）。
func TestChunkFrame_IncompleteNotEmitted(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)

	var mu sync.Mutex
	var events []PluginEvent
	s.SetEventHandler(func(e PluginEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()

	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	original := []byte(`{"type":"event","event":"activity_change","domain":"activity","data":{}}`)
	frames := splitIntoChunkFrames(t, "grp-miss", 3, original)
	require.GreaterOrEqual(t, len(frames), 2)
	// 只发第一片。
	require.NoError(t, conn.WriteJSON(frames[0]))

	// 给它足够时间：若实现有 bug（把半组当完成）这里就会收到事件。
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for _, e := range events {
		assert.NotEqual(t, "activity_change", e.Type, "缺帧的组不得冒泡半成品")
	}
	assert.Equal(t, 1, s.chunks.pending("inst-1"), "缺帧的组应仍在途等超时")
}

// TestChunkFrame_MalformedToleratedNotDisconnect 坏片不断连：后续正常帧仍能工作。
func TestChunkFrame_MalformedToleratedNotDisconnect(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()

	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	// 缺 groupId 的片：丢弃，不断连。
	require.NoError(t, conn.WriteJSON(chunkFrame("", 0, 2, "AAAA", "command_result")))
	// 非 base64 数据：整组拒收，不断连。
	require.NoError(t, conn.WriteJSON(chunkFrame("grp-bad", 0, 1, "@@@@", "command_result")))

	// 连接仍可用：发一条正常业务事件应能冒泡。
	var mu sync.Mutex
	var got bool
	s.SetEventHandler(func(e PluginEvent) {
		if e.Type == "player_join" {
			mu.Lock()
			got = true
			mu.Unlock()
		}
	})
	data, _ := json.Marshal(map[string]any{"playerName": "bob"})
	require.NoError(t, conn.WriteJSON(bridgeMessage{Type: "event", Event: "player_join", Data: data}))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got
	}, 2*time.Second, 20*time.Millisecond, "坏片之后连接必须仍然可用")
}

// TestChunkFrame_NestedChunkNotRecursed 重组出的原帧若又声明 event=chunk，按普通事件冒泡，
// 不再递归重组（防对端 bug 把读循环打穿）。
func TestChunkFrame_NestedChunkNotRecursed(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)

	var mu sync.Mutex
	var events []PluginEvent
	s.SetEventHandler(func(e PluginEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()

	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	// 原帧本身就是一个 chunk 声明（对端实现错误）。
	original := []byte(`{"type":"event","event":"chunk","data":{"groupId":"inner"}}`)
	for _, f := range splitIntoChunkFrames(t, "grp-nest", 2, original) {
		require.NoError(t, conn.WriteJSON(f))
	}

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		// 应以 chunk 类型冒泡一次（不再被当成待重组片吞掉）。
		return len(events) > 0
	}, 2*time.Second, 20*time.Millisecond)
}
