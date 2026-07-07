package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLaunchSpecForProvision_SpongeForgeKeepsMinecraftJavaLaunchModel(t *testing.T) {
	core := &CoreInfo{Runtime: &CoreRuntimeInfo{Distribution: "spongeforge", LaunchJar: "forge-1.21.1-52.1.5-server.jar"}}
	spec, err := launchSpecForProvision(ProvisionBukkitRequest{MemoryMb: 4096, JvmArgs: []string{"-XX:+UseG1GC"}}, core)
	require.NoError(t, err)
	require.Equal(t, "forge-1.21.1-52.1.5-server.jar", spec.CoreJar)
	require.Equal(t, 4096, spec.MemoryMb)
	require.Equal(t, []string{"-XX:+UseG1GC"}, spec.JvmArgs)

	_, err = launchSpecForProvision(ProvisionBukkitRequest{}, &CoreInfo{Runtime: &CoreRuntimeInfo{Distribution: "spongeforge"}})
	require.Error(t, err)
}

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
