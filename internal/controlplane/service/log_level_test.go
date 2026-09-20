package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 结构化 level= 解析：与前端 console-log-line 同一套推断，供非 MC 进程（Beacon 等 slog 全量 stderr）不再被误标 ERROR。
func TestInstanceLineLogLevel_Structured(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		line   string
		want   model.LogLevel
	}{
		{
			name:   "stderr slog INFO 不按 ERROR 兜底",
			stream: "stderr",
			line:   `time=2026-09-20T12:24:28.732+08:00 level=INFO msg=访问 方法=GET 路径=/beacon/v2/agent/registration 状态=200 耗时=885.456µs`,
			want:   model.LogLevelInfo,
		},
		{
			name:   "stderr slog WARN",
			stream: "stderr",
			line:   `time=x level=WARN msg=慢查询`,
			want:   model.LogLevelWarn,
		},
		{
			name:   "stdout slog ERROR",
			stream: "stdout",
			line:   `time=x level=error msg=失败`,
			want:   model.LogLevelError,
		},
		{
			name:   "引号包裹的 level",
			stream: "stderr",
			line:   `time=x level="debug" msg=细节`,
			want:   model.LogLevelDebug,
		},
		{
			name:   "stderr 无结构化 level 仍兜底 ERROR",
			stream: "stderr",
			line:   `plain boom`,
			want:   model.LogLevelError,
		},
		{
			name:   "stdout 无结构化 level 兜底 INFO",
			stream: "stdout",
			line:   `plain ok`,
			want:   model.LogLevelInfo,
		},
		{
			name:   "非 level 键（状态=）不得误判",
			stream: "stderr",
			line:   `状态=200 耗时=1ms`,
			want:   model.LogLevelError,
		},
		{
			name:   "level_token 不是 level 键",
			stream: "stderr",
			line:   `msg=访问 level_token=INFO`,
			want:   model.LogLevelError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, instanceLineLogLevel(tt.stream, tt.line))
		})
	}
}

// 集成：IngestInstanceOutput 落库级别跟随结构化 level=，而非一律按 stderr→error。
func TestLog_IngestInstanceOutput_StructuredLevel(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))

	node := model.Node{UUID: "node-uuid-slog", Name: "n-slog", Host: "127.0.0.1"}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID:         "inst-uuid-slog",
		Name:         "beacon",
		NodeID:       node.ID,
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "./beacon",
	}
	require.NoError(t, db.Create(&inst).Error)

	svc.Start()
	defer svc.Stop()

	svc.IngestInstanceOutput("node-uuid-slog", "inst-uuid-slog", "stderr",
		`time=2026-09-20T12:24:28.732+08:00 level=INFO msg=访问 路径=/beacon/v2/agent/registration 状态=200`+"\n"+
			`time=2026-09-20T12:24:29.000+08:00 level=WARN msg=队列偏满`+"\n"+
			`no structured level here`, 0)

	require.Eventually(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Where("instance_uuid = ?", inst.UUID).Count(&n)
		return n == 3
	}, 5*time.Second, 50*time.Millisecond)

	var entries []model.LogEntry
	require.NoError(t, db.Where("instance_uuid = ?", inst.UUID).Order("id ASC").Find(&entries).Error)
	require.Len(t, entries, 3)
	require.Equal(t, model.LogLevelInfo, entries[0].Level)
	require.Equal(t, model.LogLevelWarn, entries[1].Level)
	require.Equal(t, model.LogLevelError, entries[2].Level)
	// stream 原样保留，级别不再被 stream 独占决定。
	for _, e := range entries {
		require.Equal(t, "stderr", e.Stream)
	}
}

