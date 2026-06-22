package service

import (
	"strings"
	"testing"
)

func TestBuildServerProperties(t *testing.T) {
	props := buildServerProperties(25566, 25566, false)
	for _, want := range []string{
		"server-port=25566",
		"online-mode=false",
		"enable-query=true",
		"query.port=25566",
	} {
		if !strings.Contains(props, want) {
			t.Fatalf("server.properties 缺少 %q:\n%s", want, props)
		}
	}
	// online-mode=true 透传（独立正版服）
	if on := buildServerProperties(25566, 25566, true); !strings.Contains(on, "online-mode=true") {
		t.Fatalf("online-mode=true 未透传:\n%s", on)
	}
}
