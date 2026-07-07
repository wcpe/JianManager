package middleware

import "testing"

func TestDetermineAction_PluginBatchDeploy(t *testing.T) {
	if got := determineAction("POST", "/api/v1/plugins/batch-deploy"); got != "plugin.batchDeploy" {
		t.Fatalf("action = %q", got)
	}
}
