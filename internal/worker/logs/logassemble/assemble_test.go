package logassemble

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// TestAssemble_QueryStackCapabilities 守住「装配出的查询栈不虚报能力」。
//
// 原在 runbook 包（该包已随旧链路删除而无生产代码）。断言的是本包的装配口径：
// 未接真实 RangeClient 时，能力协商**不得**宣称支持 search——否则 CP 会以为
// 这台 Worker 能查日志，把查询打过来再拿到 UNSUPPORTED，表现为「日志页打不开」。
func TestAssemble_QueryStackCapabilities(t *testing.T) {
	cat := catalog.New(nil)
	planner := query.NewPlanner(cat, nil)
	qs := query.NewService(planner, nil, "assemble-test")
	caps := qs.GetCapabilities(query.CapabilitiesRequest{ProtocolVersion: query.DefaultProtocolVersion})

	assert.True(t, caps.Supported, "查询面本身应可用")
	for _, c := range caps.Capabilities {
		assert.NotEqual(t, "search", c,
			"未接 RangeClient 时不得宣称支持 search —— 会让 CP 把查询打过来再吃 UNSUPPORTED")
	}
}
