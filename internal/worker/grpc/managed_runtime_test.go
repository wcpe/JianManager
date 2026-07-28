package grpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/bot"
)

func TestProcessCPUPercent_FirstSampleAndInvalidIntervalAreUnavailable(t *testing.T) {
	srv := &Server{managedRuntimeCPU: make(map[int]managedRuntimeCPUBaseline)}
	base := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	require.Nil(t, srv.processCPUPercent(12, 3, base), "首帧不得伪造 CPU 零值")
	require.Nil(t, srv.processCPUPercent(12, 3.1, base), "无有效时间差不得计算 CPU")
	got := srv.processCPUPercent(12, 3.3, base.Add(time.Second))
	require.NotNil(t, got)
	require.InDelta(t, 20, *got, 1e-9)
}

func TestManagedBotCapacityMax_OnlyPublishesValidReadyCapacity(t *testing.T) {
	tests := []struct {
		name       string
		capacity   bot.BotCapacitySnapshot
		want       *int32
		wantReason string
	}{
		{name: "未就绪", capacity: bot.BotCapacitySnapshot{Ready: false, MaxBots: 50}, wantReason: "Bot Worker 尚未就绪"},
		{name: "缺少上限", capacity: bot.BotCapacitySnapshot{Ready: true}, wantReason: "Bot Worker 未报告有效容量"},
		{name: "有效容量", capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50}, want: int32Ptr(50)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := managedBotCapacityMax(tt.capacity)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantReason, reason)
		})
	}
}

func int32Ptr(value int32) *int32 {
	return &value
}
