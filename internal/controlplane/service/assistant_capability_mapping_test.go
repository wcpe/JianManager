package service

/*
 * 能力命名空间一致性门禁。
 *
 * 背景：助手侧有两套命名空间——agent 能力（instance.life / instance.configure …）与
 * RBAC 权限节点（instance.operate / instance.launchspec.write …）。
 * 修复前执行期直接拿能力名去 `HasNode`，导致：
 *   - 非平台管理员对几乎所有写工具永久 403；
 *   - `instance.write` 被映射成 `instance.configure` → 组员可改 startCommand。
 *
 * 这些用例的作用是：以后任何人新增能力/新增 action 却忘了同步三张表之一，这里就红。
 */

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCapabilityRequiredPermissions_CoversAllCapabilities 每个已知能力都必须有反查条目。
//
// 漏登记的后果是“该能力永远不可用”（fail-closed），但那是隐形故障——
// 用户只会看到工具全部失败，而不知道是漏了一张表。所以这里要求全覆盖。
func TestCapabilityRequiredPermissions_CoversAllCapabilities(t *testing.T) {
	all := []string{
		AgentCapabilityNodeRead, AgentCapabilityNodeOperate, AgentCapabilityNodeDestructive,
		AgentCapabilityInstanceRead, AgentCapabilityInstanceWrite, AgentCapabilityInstanceLife,
		AgentCapabilityInstanceCommand, AgentCapabilityInstanceProvision,
		AgentCapabilityInstanceConfigure, AgentCapabilityInstanceContent,
		AgentCapabilityInstanceDestructive,
		AgentCapabilityBotRead, AgentCapabilityBotManage, AgentCapabilityBotLoad,
		AgentCapabilityObservabilityRead,
	}
	for _, c := range all {
		nodes, ok := capabilityRequiredPermissions[c]
		require.True(t, ok, "能力 %q 缺少 required-permissions 条目（会导致该能力永远不可用）", c)
		require.NotEmpty(t, nodes, "能力 %q 的节点列表不得为空（空=无人可用）", c)
	}
}

// TestCapabilityRequiredPermissions_NameSpaceSeparation 反查表不得引用不存在的能力名。
func TestCapabilityRequiredPermissions_NameSpaceSeparation(t *testing.T) {
	// 这些是能力名而不是权限节点名，若哪个反查表把它们当节点用，说明又混了。
	capabilityOnly := []string{
		AgentCapabilityInstanceLife, AgentCapabilityInstanceConfigure,
		AgentCapabilityInstanceContent, AgentCapabilityInstanceDestructive,
		AgentCapabilityNodeOperate, AgentCapabilityObservabilityRead,
		AgentCapabilityBotLoad, AgentCapabilityInstanceProvision,
	}
	for cap, nodes := range capabilityRequiredPermissions {
		for _, n := range nodes {
			for _, bad := range capabilityOnly {
				require.NotEqual(t, bad, n,
					"能力 %q 的节点列表里出现了能力名 %q——两套命名空间又混了", cap, bad)
			}
		}
	}
}

// TestCapabilityRequiredPermissions_InstanceWriteGrantsNoConfigure
// **安全回归**：普通写权限不得授予配置（改 startCommand）能力。
//
// 修复前：`instance.write` → {instance.write, instance.configure}，
// 而平台把“改启动命令”归为高风险节点 instance.launchspec.write（只给组管理员）。
// 结果是**组员经助手就能改 startCommand = 任意命令执行**。
func TestCapabilityRequiredPermissions_InstanceWriteGrantsNoConfigure(t *testing.T) {
	grants, ok := agentCapabilitiesForPermissions["instance.write"]
	require.True(t, ok)
	for _, g := range grants {
		require.NotEqual(t, AgentCapabilityInstanceConfigure, g,
			"instance.write 不得授予 instance.configure：那等于让组员改 startCommand")
	}

	// 正向：launchspec 节点仍然能给到配置能力（否则没人能改配置了）。
	lsGrants, ok := agentCapabilitiesForPermissions["instance.launchspec.write"]
	require.True(t, ok, "instance.launchspec.write 必须映射到配置能力")
	require.Contains(t, lsGrants, AgentCapabilityInstanceConfigure)
}

// TestCanWithCapability_MapsThroughNodes 能力判定真的落到权限节点上。
func TestCanWithCapability_MapsThroughNodes(t *testing.T) {
	// 组员：有 instance.write / instance.operate，没有 launchspec / delete。
	member := &UserAccess{Nodes: map[string]struct{}{
		"instance.read": {}, "instance.write": {}, "instance.operate": {},
	}}
	require.True(t, member.CanWithCapability(AgentCapabilityInstanceWrite))
	require.True(t, member.CanWithCapability(AgentCapabilityInstanceLife), "instance.operate 应能给到启停能力")
	require.False(t, member.CanWithCapability(AgentCapabilityInstanceConfigure),
		"没有 instance.launchspec.write 就不得具备配置能力")
	require.False(t, member.CanWithCapability(AgentCapabilityInstanceDestructive),
		"没有 instance.delete 就不得具备销毁能力")

	// 组管理员：补上 launchspec。
	admin := &UserAccess{Nodes: map[string]struct{}{
		"instance.read": {}, "instance.write": {}, "instance.operate": {},
		"instance.launchspec.write": {}, "instance.delete": {},
	}}
	require.True(t, admin.CanWithCapability(AgentCapabilityInstanceConfigure))
	require.True(t, admin.CanWithCapability(AgentCapabilityInstanceDestructive))

	// 平台管理员：全开。
	platform := &UserAccess{IsPlatformAdmin: true}
	require.True(t, platform.CanWithCapability(AgentCapabilityNodeDestructive))

	// 未知能力 fail-closed。
	require.False(t, admin.CanWithCapability("not.a.real.capability"))
	// nil 安全。
	var nilAccess *UserAccess
	require.False(t, nilAccess.CanWithCapability(AgentCapabilityInstanceRead))
}

// TestCanWithCapability_ReintegratesWithGrantSide 授予侧与校验侧互为镜像。
//
// 不要求“有节点就一定有能力”（映射是单向的、故意留窄），但要求：
// 授予侧给出的每一个能力，都必须能在反查表里找到落点。否则就是“给了一个永远用不了的能力”。
func TestCanWithCapability_ReintegratesWithGrantSide(t *testing.T) {
	for perm, caps := range agentCapabilitiesForPermissions {
		for _, c := range caps {
			_, ok := capabilityRequiredPermissions[c]
			require.True(t, ok, "权限 %q 授予的能力 %q 没有反查条目", perm, c)
		}
	}
}
