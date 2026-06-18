package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBuildTerminalWSURL 覆盖终端代理 WS URL 的 scheme 选择与 baseURL 回退。
func TestBuildTerminalWSURL(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		requestHost string
		secure      bool
		want        string
	}{
		{"非加密访问 → ws", "ws://localhost:8080", "192.168.1.100:8080", false, "ws://192.168.1.100:8080/ws/terminal"},
		{"HTTPS/反代访问 → wss", "ws://localhost:8080", "panel.example.com", true, "wss://panel.example.com/ws/terminal"},
		{"空 Host 回退 baseURL", "ws://localhost:8080", "", false, "ws://localhost:8080/ws/terminal"},
		{"空 Host 时 secure 不改写 baseURL", "wss://panel.example.com", "", true, "wss://panel.example.com/ws/terminal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildTerminalWSURL(tt.baseURL, tt.requestHost, tt.secure))
		})
	}
}
