package main

import (
	"testing"
)

// TestMCPTools_AlignOpsContract 工具数与名称对齐 FR-388 运维契约（10 项）。
func TestMCPTools_AlignOpsContract(t *testing.T) {
	want := map[string]struct{}{
		"agent_whoami": {}, "agent_list_nodes": {}, "agent_list_instances": {},
		"agent_get_instance": {}, "agent_get_instance_metrics": {},
		"instance_start": {}, "instance_stop": {}, "instance_restart": {},
		"node_maintenance_enter": {}, "node_maintenance_leave": {},
	}
	tools := RegisteredTools()
	if len(tools) != len(want) {
		t.Fatalf("MCP 工具数=%d，期望 %d", len(tools), len(want))
	}
	for _, tool := range tools {
		if _, ok := want[tool.Name]; !ok {
			t.Fatalf("未知工具 %q，未在 Agent 运维契约内", tool.Name)
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("契约工具未注册: %v", want)
	}
}
