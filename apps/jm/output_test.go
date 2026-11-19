package main

import (
	"math"
	"testing"
)

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("北京节点测试", 4)
	if got != "北京节…" {
		t.Fatalf("截断结果错误: %q", got)
	}
}

func TestFormatScalarJSONMarshalErrorFallsBackToText(t *testing.T) {
	got := formatScalar(map[string]any{"value": math.Inf(1)})
	if got == "" {
		t.Fatal("JSON 序列化失败时不应返回空文本")
	}
}

func TestNodeStatusText(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{name: "离线", input: float64(0), want: "offline"},
		{name: "在线", input: float64(1), want: "online"},
		{name: "未知状态", input: float64(3), want: "3"},
		{name: "非数值状态", input: "custom", want: "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeStatusText(tt.input); got != tt.want {
				t.Fatalf("nodeStatusText(%v) = %q，期望 %q", tt.input, got, tt.want)
			}
		})
	}
}
