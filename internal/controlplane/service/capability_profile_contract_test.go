package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-445 / ADR-091：能力画像有三份登记（后端注册表、前端兜底表、devmock 真源），
// 三者必须逐画像一致。此前只有「前端 ↔ devmock」有机器看守，「后端 ↔ 前端」缺看守，
// 后端悄悄改了能力集合而前端兜底表没跟，不会有测试失败。
//
// 本测试锁定仓库根的共享契约样本 `contracts/capability-profiles.json`：
// 后端此处 assert 注册表 == 样本，前端 `src/lib/capabilities.contract.test.ts`
// assert 前端兜底表 == 样本 → 二者传递性地互相看守（后端 ↔ 前端）。
//
// 改动画像后重新生成样本：
//
//	UPDATE_CAPABILITY_CONTRACT=1 go test ./internal/controlplane/service/ -run TestCapabilityProfile_SharedContractFixture
type contractProfile struct {
	MCSemantics  bool                `json:"mcSemantics"`
	Capabilities []string            `json:"capabilities"`
	Sources      map[string][]string `json:"sources,omitempty"`
}

type contractDoc struct {
	Fallback contractProfile            `json:"fallback"`
	Profiles map[string]contractProfile `json:"profiles"`
}

func toContractProfile(p model.InstanceCapabilityProfile) contractProfile {
	caps := make([]string, 0, len(p.Capabilities))
	for _, c := range p.Capabilities {
		caps = append(caps, string(c))
	}
	var sources map[string][]string
	if len(p.Sources) > 0 {
		sources = make(map[string][]string, len(p.Sources))
		for k, v := range p.Sources {
			ds := make([]string, 0, len(v))
			for _, d := range v {
				ds = append(ds, string(d))
			}
			sources[string(k)] = ds
		}
	}
	return contractProfile{MCSemantics: p.MCSemantics, Capabilities: caps, Sources: sources}
}

// buildContractDoc 由注册表现算契约文档；JSON map 键序由 encoding/json 保证确定。
func buildContractDoc() contractDoc {
	doc := contractDoc{
		Fallback: toContractProfile(universalFallback),
		Profiles: make(map[string]contractProfile, len(capabilityRegistry)),
	}
	for k, p := range capabilityRegistry {
		doc.Profiles[string(k.Type)+":"+string(k.Role)] = toContractProfile(p)
	}
	return doc
}

func capabilityContractPath() string {
	return filepath.Join("..", "..", "..", "contracts", "capability-profiles.json")
}

func TestCapabilityProfile_SharedContractFixture(t *testing.T) {
	want, err := json.MarshalIndent(buildContractDoc(), "", "  ")
	require.NoError(t, err)
	want = append(want, '\n')

	if os.Getenv("UPDATE_CAPABILITY_CONTRACT") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(capabilityContractPath()), 0o755))
		require.NoError(t, os.WriteFile(capabilityContractPath(), want, 0o644))
		t.Logf("已重新生成共享契约样本: %s", capabilityContractPath())
		return
	}

	got, err := os.ReadFile(capabilityContractPath())
	require.NoError(t, err, "共享契约样本缺失；设 UPDATE_CAPABILITY_CONTRACT=1 重新生成")
	assert.Equal(t, string(want), string(got),
		"后端画像注册表与共享契约样本不一致：同步 service/capability_profile.go、"+
			"lib/capabilities.ts、devmock capability-profile.ts 后重新生成样本")
}
