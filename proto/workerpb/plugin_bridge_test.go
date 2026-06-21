package workerpb

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestPluginEvent_RoundTrip 固化插件桥事件 message 的线上往返：Marshal→Unmarshal 后全字段一致。
// 这同时是 worker.pb.go 由 protoc 正确重生成（而非 sed 改坏）的回归闸——字段号冲突或部分重生成
// 会让某些字段在往返后丢失/串位，本用例即可捕获（见 ADR-016 与 commit c1cb5af 教训）。
func TestPluginEvent_RoundTrip(t *testing.T) {
	src := &PluginEvent{
		InstanceUuid: "inst-uuid-1",
		Type:         "player_join",
		Timestamp:    1718000000,
		PlayerName:   "Steve",
		PlayerUuid:   "00000000-0000-0000-0000-000000000001",
		Message:      "hello world",
		Server:       "lobby",
		FromServer:   "survival",
		ToServer:     "lobby",
		Platform:     "bukkit",
		Version:      "1.2.3",
		RequestId:    "req-7",
		RawJson:      `{"k":"v"}`,
	}

	wire, err := proto.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal PluginEvent 失败: %v", err)
	}

	var got PluginEvent
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("Unmarshal PluginEvent 失败: %v", err)
	}

	if !proto.Equal(src, &got) {
		t.Fatalf("PluginEvent 往返不一致:\n src=%v\n got=%v", src, &got)
	}

	// 逐字段抽查高位字段号（10~13），确保 protoc 生成的 13 个字段全部覆盖、无尾部截断。
	if got.Platform != src.Platform || got.Version != src.Version ||
		got.RequestId != src.RequestId || got.RawJson != src.RawJson {
		t.Fatalf("高位字段往返丢失: %+v", &got)
	}
}

// TestPluginCommand_RoundTrip 固化下行治理指令 message 的往返（含 repeated args）。
func TestPluginCommand_RoundTrip(t *testing.T) {
	src := &PluginCommand{
		Action:    "kick",
		Target:    "Alex",
		Reason:    "afk",
		Args:      []string{"--silent", "--log"},
		RequestId: "cmd-42",
	}

	wire, err := proto.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal PluginCommand 失败: %v", err)
	}

	var got PluginCommand
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("Unmarshal PluginCommand 失败: %v", err)
	}

	if !proto.Equal(src, &got) {
		t.Fatalf("PluginCommand 往返不一致:\n src=%v\n got=%v", src, &got)
	}
	if len(got.Args) != 2 {
		t.Fatalf("repeated args 往返丢失: %v", got.Args)
	}
}

// TestSendPluginCommand_RequestResponse_RoundTrip 固化指令下发请求/响应（含嵌套 PluginCommand）往返。
func TestSendPluginCommand_RequestResponse_RoundTrip(t *testing.T) {
	req := &SendPluginCommandRequest{
		InstanceUuid: "inst-uuid-2",
		Command:      &PluginCommand{Action: "ban", Target: "Grief", RequestId: "cmd-99"},
	}
	wire, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal SendPluginCommandRequest 失败: %v", err)
	}
	var gotReq SendPluginCommandRequest
	if err := proto.Unmarshal(wire, &gotReq); err != nil {
		t.Fatalf("Unmarshal SendPluginCommandRequest 失败: %v", err)
	}
	if !proto.Equal(req, &gotReq) {
		t.Fatalf("SendPluginCommandRequest 往返不一致: %v vs %v", req, &gotReq)
	}
	if gotReq.Command == nil || gotReq.Command.Target != "Grief" {
		t.Fatalf("嵌套 PluginCommand 往返丢失: %+v", gotReq.Command)
	}

	resp := &SendPluginCommandResponse{Success: true, RequestId: "cmd-99"}
	rw, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal SendPluginCommandResponse 失败: %v", err)
	}
	var gotResp SendPluginCommandResponse
	if err := proto.Unmarshal(rw, &gotResp); err != nil {
		t.Fatalf("Unmarshal SendPluginCommandResponse 失败: %v", err)
	}
	if !proto.Equal(resp, &gotResp) {
		t.Fatalf("SendPluginCommandResponse 往返不一致: %v vs %v", resp, &gotResp)
	}
}

// TestQueryServerState_RoundTrip 固化全状态查询请求/响应骨架往返。
func TestQueryServerState_RoundTrip(t *testing.T) {
	req := &QueryServerStateRequest{InstanceUuid: "inst-uuid-3"}
	rw, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal QueryServerStateRequest 失败: %v", err)
	}
	var gotReq QueryServerStateRequest
	if err := proto.Unmarshal(rw, &gotReq); err != nil {
		t.Fatalf("Unmarshal QueryServerStateRequest 失败: %v", err)
	}
	if gotReq.InstanceUuid != "inst-uuid-3" {
		t.Fatalf("QueryServerStateRequest 往返丢失: %v", &gotReq)
	}

	resp := &QueryServerStateResponse{Success: true, Connected: true, StateJson: `{"online":3}`}
	rw2, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal QueryServerStateResponse 失败: %v", err)
	}
	var gotResp QueryServerStateResponse
	if err := proto.Unmarshal(rw2, &gotResp); err != nil {
		t.Fatalf("Unmarshal QueryServerStateResponse 失败: %v", err)
	}
	if !proto.Equal(resp, &gotResp) {
		t.Fatalf("QueryServerStateResponse 往返不一致: %v vs %v", resp, &gotResp)
	}
}
