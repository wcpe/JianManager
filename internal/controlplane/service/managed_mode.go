package service

import (
	"encoding/json"
	"fmt"
	"time"
)

// 外部控制面托管模式（ServerProbe PR#64 / FR-42~47）的 CP 侧实现。
//
// ## 与既有插件桥（ADR-016）的关系
// 旧链路：CP 签发 scope=plugin-bridge 的 JWT → 写入探针 config.yml 的 `bridge:` 段 →
// 探针反向连 ws://<worker>:9102/ws/plugin-bridge?token=&instance=。
// 探针只经桥上报业务事件、接收治理指令，**指标仍走 HTTP 拉取 /metrics**，
// 且探针的本地面板/MCP/metrics 三个监听面都开着。
//
// 托管模式（PR#64）：探针以 `plugins/ServerProbe/agent-identity.json` 为唯一开关，
// 从该文件读 endpoint/credential/instanceId 建连；连上后 hello 声明 managed=true 与
// metric_push/config_push/config_state 三个能力位，此后：
//   - 指标改由 `metric_snapshot` 经桥推送（探针不再开 /metrics，零监听承诺）
//   - CP 可下发 `config_push` 热改探针配置（不必重启宿主）
//   - 探针周期上行 `config_state` 汇报托管状态；应用结果回 `config_applied`
//   - MCP 工具面经 `core.mcp` 走桥（零监听下保住事故诊断）
//
// ## 兼容性铁律
// 身份文件**存在即托管**，删除即回退独立模式。因此本文件只负责"生成并下发身份文件"，
// 不做任何"猜测探针版本"的分支——老版本探针不认识 agent-identity.json，会忽略它并按
// config.yml 的 `bridge:` 段走独立模式，行为与改造前完全一致（无破坏性回退）。

// ManagedIdentityFileName 是探针侧的托管身份文件名（协议规格 §3.2，路径固定）。
// 探针从插件数据目录读取它：`plugins/ServerProbe/agent-identity.json`。
const ManagedIdentityFileName = "agent-identity.json"

// managedIdentitySchemaVersion 是探针当前唯一接受的格式版本。
// 探针侧校验为严格相等：其它值一律按"版本未知"降级独立模式 + WARN。
const managedIdentitySchemaVersion = 1

// ManagedAgentIdentity 是下发到探针的身份文件内容（协议规格 §3.2）。
//
// 字段名大小写**必须逐字一致**——探针用固定键名解析，改一个字母就整份文件作废。
// 探针侧只有 provider/endpoint/instanceId/credential 四项必填，capabilities 与 createdAt
// 缺失不算损坏；但仍全部下发，便于探针日志与排障。
//
// ## credential 的不透明约定
// 探针对 credential **不做任何解析**（不解析 JWT claims、不做前缀判断、连 trim 都不做），
// 只原样拼进握手 query 的 `token=`。因此这里复用既有插件桥 JWT（scope=plugin-bridge）
// 即可——Worker 侧的握手校验逻辑完全不用改。
// 换一种凭据形态也不会更好：真正的信任边界是 Worker 的 token 校验，不是格式。
type ManagedAgentIdentity struct {
	SchemaVersion int      `json:"schemaVersion"`
	Provider      string   `json:"provider"`
	Endpoint      string   `json:"endpoint"`
	InstanceID    string   `json:"instanceId"`
	Credential    string   `json:"credential"`
	Capabilities  []string `json:"capabilities"`
	CreatedAt     string   `json:"createdAt"`
}

// managedProviderName 是本控制面对外自称的 provider 标识。
//
// 探针把 provider 当**纯数据**（不参与任何分支判断），所以这里取产品名即可；
// 它的作用是可观测性——一台机器同时接多个控制面时能一眼看出是哪家在托管。
const managedProviderName = "jianmanager"

// managedCapabilities 是 CP 侧声明自己支持的能力位，随身份文件下发。
//
// **探针完全不消费该字段**（只在 JSON 编解码与 toString 里出现），真实能力协商走
// hello / core.bridge_status 的 capabilities。这里填全仍有两个用处：
//   1. 人读排障：一眼看出这条身份是按哪个协议代写的；
//   2. 未来若探针改为消费，不至于下发空数组把能力关掉。
var managedCapabilities = []string{"metric_push", "config_push", "config_state", "mcp_bridge"}

// ManagedModeService 生成托管身份文件。
//
// 刻意做成**无状态 + 纯函数式**：它不碰数据库、不缓存，只把调用方给的输入拼成协议要求的
// JSON 文本。这样它的每条字段都能被单测逐字断言（与探针侧 IdentityJsonTest 对齐）。
type ManagedModeService struct{}

// NewManagedModeService 创建托管模式服务。
func NewManagedModeService() *ManagedModeService { return &ManagedModeService{} }

// BuildIdentity 组装一份托管身份。
//
// @param endpoint    探针反向 WS 接入地址，形如 ws://<host>:9102/ws/plugin-bridge。
//                    必须带 `ws://` 前缀——探针只支持明文 ws，`wss://` 会在建连期被拒绝。
//                    地址里**不要自带 query**：探针会整段丢弃并用自己的 `token=&instance=` 覆盖。
// @param instanceUUID 实例 UUID；它同时是握手 query 的 `instance=` 与所有上行帧的 `instance` 字段。
// @param credential  不透明凭据（此处为 scope=plugin-bridge 的 JWT）。
// @return 身份结构体；调用方再经 MarshalIdentity 取 JSON 文本。
func (s *ManagedModeService) BuildIdentity(endpoint, instanceUUID, credential string) ManagedAgentIdentity {
	return ManagedAgentIdentity{
		SchemaVersion: managedIdentitySchemaVersion,
		Provider:      managedProviderName,
		Endpoint:      endpoint,
		InstanceID:    instanceUUID,
		Credential:    credential,
		Capabilities:  managedCapabilities,
		CreatedAt:     time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// MarshalIdentity 把身份序列化为探针期望的 JSON 文本。
//
// 输出为紧凑单行（探针侧是 nj 流式解析，格式化与否都接受；单行便于日志逐行对照）。
// 末尾补换行：探针走原子写（tmp → fsync → rename），补换行符合文本文件的通行约定。
func (s *ManagedModeService) MarshalIdentity(identity ManagedAgentIdentity) (string, error) {
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("序列化托管身份失败: %w", err)
	}
	return string(raw) + "\n", nil
}

// BuildIdentityFile 是 BuildIdentity + MarshalIdentity 的组合入口，供下发链路直接调用。
func (s *ManagedModeService) BuildIdentityFile(endpoint, instanceUUID, credential string) (string, error) {
	return s.MarshalIdentity(s.BuildIdentity(endpoint, instanceUUID, credential))
}
