// Package runbook — T11 本地 Runbook 证据（不替代远程真机验收）。
package runbook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// Runbook C：资产校验、loopback-only 客户端、防递归 sink。
func TestRunbookC_SupervisorGuards(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "victoria-logs-fake")
	require.NoError(t, os.WriteFile(exe, []byte("fake"), 0o755))
	require.Error(t, vlsup.VerifyAsset(exe, "deadbeef"))

	_, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://example.com:9428", Username: "u", Password: "p"})
	require.Error(t, err)

	assert.False(t, vlsup.IndependentLoggerSink{}.WritesIntoVL())
}

// 装配：UnimplementedRangeClient 不宣称 search 能力。
func TestAssemble_QueryStackCapabilities(t *testing.T) {
	cat := catalog.New(nil)
	planner := query.NewPlanner(cat, nil)
	qs := query.NewService(planner, nil, "runbook-build")
	caps := qs.GetCapabilities(query.CapabilitiesRequest{ProtocolVersion: query.DefaultProtocolVersion})
	assert.True(t, caps.Supported)
	for _, c := range caps.Capabilities {
		assert.NotEqual(t, "search", c)
	}
}
