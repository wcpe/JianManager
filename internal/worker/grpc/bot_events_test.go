package grpc

import (
	"encoding/json"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/bot"
)

func TestBotWorkerEventToProtoFiltersState(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt: "bot-state",
		Bots: []bot.BotState{
			{ID: "bot-a", Status: "connected", Health: 20, Food: 18, Behavior: "guard"},
			{ID: "bot-b", Status: "connecting", Behavior: "idle"},
		},
	}

	got := botWorkerEventToProto(event, "bot-a")
	if len(got) != 1 {
		t.Fatalf("期望 1 条事件，实际 %d", len(got))
	}
	if got[0].BotUuid != "bot-a" || got[0].Type != "state" {
		t.Fatalf("事件字段不正确: %+v", got[0])
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(got[0].Data), &data); err != nil {
		t.Fatalf("状态 data 不是 JSON: %v", err)
	}
	if data["status"] != "connected" || data["behavior"] != "guard" {
		t.Fatalf("状态 data 不正确: %v", data)
	}
}

func TestBotWorkerEventToProtoOmitsMissingStateMetrics(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:  "bot-state",
		Bots: []bot.BotState{{ID: "bot-a", Status: "disconnected"}},
	}

	got := botWorkerEventToProto(event, "bot-a")
	if len(got) != 1 {
		t.Fatalf("期望 1 条事件，实际 %d", len(got))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(got[0].Data), &data); err != nil {
		t.Fatalf("状态 data 不是 JSON: %v", err)
	}
	if _, ok := data["health"]; ok {
		t.Fatalf("缺失 health 时不应输出 0: %v", data)
	}
	if _, ok := data["food"]; ok {
		t.Fatalf("缺失 food 时不应输出 0: %v", data)
	}
}

func TestBotWorkerEventToProtoForwardsErrors(t *testing.T) {
	event := &bot.BotWorkerEvent{Evt: "bot-error", BotID: "bot-a", Error: "连接失败"}

	got := botWorkerEventToProto(event, "bot-a")
	if len(got) != 1 {
		t.Fatalf("期望 1 条事件，实际 %d", len(got))
	}
	if got[0].Type != "error" {
		t.Fatalf("期望 error 事件，实际 %s", got[0].Type)
	}
	if !json.Valid([]byte(got[0].Data)) {
		t.Fatalf("错误 data 不是 JSON: %s", got[0].Data)
	}
}
