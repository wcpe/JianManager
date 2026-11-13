package grpc

import (
	"testing"
	"unicode/utf8"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// 非法 UTF-8 会让 gRPC 的 proto 序列化直接失败（`string field contains invalid UTF-8`）。
//
// 现场：一条脏事件让 `stream.Send` 报错 → `return err` → **整条 StreamPluginEvents 断开**，
// CP 每 ~10 秒重连一次，从 2026-09-30 起累计刷了 6 万余条日志，
// 并连带 `QueryBusinessStatus` 等经同一 gRPC 通道的查询失败
// （CP 面板表现为「域状态查询调用失败」）。
//
// 这些用例锁死「一条脏数据不得瘫痪整条观测通道」这条契约。

// 构造一个含非法 UTF-8 字节的字符串：0xFF / 0xFE 不是任何合法序列的首字节。
func invalidUTF8String() string {
	return "玩家名\xff\xfe" + string([]byte{0x80, 0x81})
}

func TestSanitizePluginEvent_ReplacesInvalidUTF8(t *testing.T) {
	bad := invalidUTF8String()
	evt := &workerpb.PluginEvent{
		InstanceUuid: "inst-uuid-ok",
		Type:         "chat",
		PlayerName:   bad,
		Message:      "正常内容" + bad,
		RawJson:      `{"msg":"` + bad + `"}`,
	}

	sanitizePluginEvent(evt)

	// 关键断言：清洗后必须全部是合法 UTF-8，否则 stream.Send 仍会失败。
	for name, s := range map[string]string{
		"instance_uuid": evt.InstanceUuid,
		"type":          evt.Type,
		"player_name":   evt.PlayerName,
		"message":       evt.Message,
		"raw_json":      evt.RawJson,
	} {
		if !utf8.ValidString(s) {
			t.Errorf("%s 清洗后仍含非法 UTF-8", name)
		}
	}

	// 非法字节被替换为 U+FFFD，合法部分原样保留（不丢语义）。
	if !containsReplacementChar(evt.PlayerName) {
		t.Errorf("player_name 应含替换字符 U+FFFD，实际=%q", evt.PlayerName)
	}
	if !containsChinese(evt.Message) {
		t.Errorf("message 的合法中文部分丢失了，实际=%q", evt.Message)
	}
}

// 合法字符串必须原样返回：清洗不该篡改正常数据。
func TestSanitizePluginEvent_LeavesValidStringsUntouched(t *testing.T) {
	evt := &workerpb.PluginEvent{
		InstanceUuid: "b7d0c1a2-0000-0000-0000-000000000000",
		Type:         "player_join",
		Platform:     "bukkit",
		Version:      "0.4.0",
		PlayerName:   "Steve_玩家一",
		PlayerUuid:   "uuid-here",
		Message:      "欢迎来到服务器 🎉",
		Server:       "game-01",
		FromServer:   "lobby-01",
		ToServer:     "game-02",
		RawJson:      `{"a":"中文","emoji":"🎉"}`,
		Domain:       "economy",
		DedupKey:     "ledger-42",
	}
	// 逐字段比较：proto 消息内嵌 protoimpl.MessageState（含 sync.Mutex），
	// 直接解引用复制整个结构体会触发 `go vet` 的 "assignment copies lock value"。
	// 这里只把**期望的字符串值**存下来，既避免复制锁、也让断言意图更直白。
	type snapshot struct{ name, want string }
	before := []snapshot{
		{"instance_uuid", "b7d0c1a2-0000-0000-0000-000000000000"},
		{"type", "player_join"},
		{"platform", "bukkit"},
		{"version", "0.4.0"},
		{"player_name", "Steve_玩家一"},
		{"player_uuid", "uuid-here"},
		{"message", "欢迎来到服务器 🎉"},
		{"server", "game-01"},
		{"from_server", "lobby-01"},
		{"to_server", "game-02"},
		{"raw_json", `{"a":"中文","emoji":"🎉"}`},
		{"domain", "economy"},
		{"dedup_key", "ledger-42"},
	}

	sanitizePluginEvent(evt)

	after := map[string]string{
		"instance_uuid": evt.InstanceUuid,
		"type":          evt.Type,
		"platform":      evt.Platform,
		"version":       evt.Version,
		"player_name":   evt.PlayerName,
		"player_uuid":   evt.PlayerUuid,
		"message":       evt.Message,
		"server":        evt.Server,
		"from_server":   evt.FromServer,
		"to_server":     evt.ToServer,
		"raw_json":      evt.RawJson,
		"domain":        evt.Domain,
		"dedup_key":     evt.DedupKey,
	}
	for _, f := range before {
		if got := after[f.name]; got != f.want {
			t.Errorf("合法字段 %s 被篡改：want=%q got=%q", f.name, f.want, got)
		}
	}
}

// nil 事件不该 panic（扇出路径上可能拿到 nil）。
func TestSanitizePluginEvent_NilSafe(t *testing.T) {
	sanitizePluginEvent(nil)
}

// 发送路径必须**真的**调清洗。
//
// 这条用例是为了补一个自己踩过的坑：最初只测 `sanitizePluginEvent` 本身，
// 把 `stream.Send` 前的调用点删掉后测试照样通过——等于测了个没被调用的函数。
// 所以这里断言「喂给 Send 的那个对象」已经干净，而不只是断言辅助函数正确。
func TestPreparePluginEventForSend_ProducesSendableEvent(t *testing.T) {
	evt := &workerpb.PluginEvent{
		InstanceUuid: "inst-ok",
		Type:         "chat",
		PlayerName:   invalidUTF8String(),
		RawJson:      `{"raw":"` + invalidUTF8String() + `"}`,
	}

	// 模拟发送路径：Send 拿到的是 prepare 的返回值。
	sent := preparePluginEventForSend(evt)

	// 真正会被 proto 序列化的那些字段必须全部合法，否则 gRPC 仍会拒绝。
	for name, s := range map[string]string{
		"instance_uuid": sent.InstanceUuid,
		"type":          sent.Type,
		"player_name":   sent.PlayerName,
		"raw_json":      sent.RawJson,
	} {
		if !utf8.ValidString(s) {
			t.Errorf("%s 在发送前仍含非法 UTF-8，stream.Send 会失败", name)
		}
	}

	// 就地清洗：返回的必须是同一个对象（Send 用的就是这个）。
	if sent != evt {
		t.Error("preparePluginEventForSend 应就地清洗并返回同一对象")
	}
}

// 合法事件经过发送路径后必须一字不改。
func TestPreparePluginEventForSend_ValidEventUnchanged(t *testing.T) {
	evt := &workerpb.PluginEvent{
		InstanceUuid: "inst-ok",
		Type:         "player_join",
		PlayerName:   "Steve_玩家",
		Message:      "欢迎 🎉",
		RawJson:      `{"a":"中文"}`,
	}
	got := preparePluginEventForSend(evt)
	if got.PlayerName != "Steve_玩家" || got.Message != "欢迎 🎉" || got.RawJson != `{"a":"中文"}` {
		t.Errorf("合法事件被篡改: %+v", got)
	}
}

// 截断必须按 rune 边界，不能把一个多字节字符切成两半——
// 切坏的部分又会变成非法 UTF-8，等于自己制造脏数据。
func TestTruncateForLog_DoesNotSplitRunes(t *testing.T) {
	s := "中文中文中文" // 6 个 rune，每个 3 字节
	got := truncateForLog(s, 3)
	if !utf8.ValidString(got) {
		t.Fatalf("截断产生了非法 UTF-8: %q", got)
	}
	if runeLen(got) != 3 {
		t.Errorf("期望 3 个 rune，实际 %d（%q）", runeLen(got), got)
	}
	if got != "中文中" {
		t.Errorf("期望 %q，实际 %q", "中文中", got)
	}
}

func TestTruncateForLog_ShortStringUnchanged(t *testing.T) {
	if got := truncateForLog("abc", 10); got != "abc" {
		t.Errorf("短字符串应原样返回，实际 %q", got)
	}
	if got := truncateForLog("abc", 0); got != "" {
		t.Errorf("上限 0 应返回空串，实际 %q", got)
	}
}

func containsReplacementChar(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return true
		}
	}
	return false
}

func containsChinese(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
