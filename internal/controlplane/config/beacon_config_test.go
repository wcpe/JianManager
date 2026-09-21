package config

import (
	"strings"
	"testing"
)

// FR-443：启用推送时的必需项 fail-fast。
// 原则是「明确 opt-in 才校验」——未启用推送的部署不受任何影响。
func TestValidateBeaconConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     BeaconConfig
		wantErr string // 期望错误包含的子串；空表示期望通过
	}{
		{
			name: "未启用推送时零校验（可选协同，绝非依赖）",
			cfg:  BeaconConfig{},
		},
		{
			name: "未启用推送时即使 endpoint 与 namespace 都缺也通过",
			cfg:  BeaconConfig{PullEnabled: true},
		},
		{
			name:    "启用推送但缺 endpoint 拒绝",
			cfg:     BeaconConfig{PushEnabled: true, Namespace: "prod"},
			wantErr: "beacon.endpoint",
		},
		{
			name:    "启用推送但缺 namespace 拒绝",
			cfg:     BeaconConfig{PushEnabled: true, Endpoint: "http://beacon.internal:8090"},
			wantErr: "beacon.namespace",
		},
		{
			name:    "空白字符不算已配置",
			cfg:     BeaconConfig{PushEnabled: true, Endpoint: "   ", Namespace: "  "},
			wantErr: "beacon.endpoint",
		},
		{
			name: "启用推送且两项齐备通过",
			cfg:  BeaconConfig{PushEnabled: true, Endpoint: "http://beacon.internal:8090", Namespace: "prod"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBeaconConfig(tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错（含 %q），实际通过", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息未含 %q，实际: %v", tc.wantErr, err)
			}
		})
	}
}
