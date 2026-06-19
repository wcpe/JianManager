package service

import (
	"strings"
	"testing"
)

func TestBuildServerProperties(t *testing.T) {
	props := buildServerProperties(25566, 25576, 25566, "secret123", false)
	for _, want := range []string{
		"server-port=25566",
		"online-mode=false",
		"enable-rcon=true",
		"rcon.port=25576",
		"rcon.password=secret123",
		"enable-query=true",
		"query.port=25566",
	} {
		if !strings.Contains(props, want) {
			t.Fatalf("server.properties 缺少 %q:\n%s", want, props)
		}
	}
	// online-mode=true 透传（独立正版服）
	if on := buildServerProperties(25566, 25576, 25566, "x", true); !strings.Contains(on, "online-mode=true") {
		t.Fatalf("online-mode=true 未透传:\n%s", on)
	}
}

func TestRandRCONPassword(t *testing.T) {
	a, b := randRCONPassword(), randRCONPassword()
	if a == "" || a == b {
		t.Fatalf("rcon 密码应随机非空且不重复: a=%q b=%q", a, b)
	}
}
