# ADR-103: Agent 运维知识的双通道分发与工具风险标注

- **日期**: 2026-11-12
- **状态**: accepted
- **关联**: [ADR-076](076-agent-token-and-mcp-authority.md)（策略真源）· [ADR-080](080-agent-capability-policy-v2.md)（能力策略 v2）· spec `docs/specs/agent-safety-gate/`

## 上下文

ADR-080 决策 4 已确立「service action 目录是 action→capability→scope 的唯一真源」。
但实践中有两处缺口：

1. **MCP 工具缺少机器可读的风险等级**。94 个工具只有一句 Description，客户端无法在调用前
   判断哪些是破坏性操作。`Operation=destructive` 的信息已存在 catalog，却没有投影出去，
   导致 agent 只能凭工具名猜测后果。
2. **业务知识无处存放**。工具描述只能说明「能做什么」，无法承载「什么状态不能删」
   「维护模式与停机的先后顺序」「策略拒绝为什么是 HTTP 200」这类必须靠文档的知识。
   agent 只能凭工具签名推测，误操作风险高。

此外，本轮曾尝试过两个后来被否决的方向，记录于此以免重走：

- **把 CLI 命令描述放进 catalog 并让 `jm` 从它生成**。实测会把 `apps/jmagent`
  从 0 个内部包依赖拖成 16 个（含 gRPC / GORM / workerpb），违反该包
  「仅经 HTTPS、不链 gRPC/DB」的自身声明。CLI 命令面改为手写 + 独立演进。
- **把 skill 包装成 `skill_list` / `skill_read` 两个 MCP 工具**。这让一篇静态文档
  变成需要 Token 授权、走 JSON-RPC 的接口，而文档本身不含敏感信息，
  属于把简单问题复杂化。

## 决策

1. **风险标注由 catalog 派生，不手写**。
   `ToolDef` 增加 `Annotations`，在投影点（`withAnnotations`）按 `Operation` 自动填充
   `readOnlyHint` / `destructiveHint` / `openWorldHint`；`RequiresConfirm` 且有确认字段时
   额外透出 `confirmField`。手写 `ToolDef` 不感知该字段，避免与 catalog 漂移。

2. **`Summary` 留在 catalog，只服务 MCP**。
   每个 action 必须有一句中文摘要，供工具描述使用。曾同时服务 CLI 帮助，
   CLI 命令面独立后该字段收窄为 MCP 专用。

3. **运维知识采用双通道分发，不用 MCP 工具包装**。
   仓库根 `skill/` 目录既是权威源，也是 CP 静态路由的内容源：
   - **GitHub 仓库**：`skill/SKILL.md` + `skill/references/*.md`，可直接 clone 或
     `npx skills add <repo>` 安装到宿主（对齐 `gh skill install` 与 Cloudflare 做法）
   - **CP 静态路由**：`GET /api/v1/skill/<path>` 匿名可读，供纯内网环境
   - 两处内容完全一致，不存在第二份真源

4. **skill 内容不需要鉴权**。
   文档只描述已公开的工具契约与操作注意事项，不含密钥、内网地址或业务数据。
   把它置于 Token 授权之后不增加安全性，只增加 agent 的使用成本。

## 理由

- **风险标注是硬约束，文档是软约束**：客户端能读到 `destructiveHint` 才会在调用前停下来问用户；
  只写在文档里的警告会被跳过。
- **skill 是宿主侧静态文件，不是服务端接口**。GitHub（`gh skill install`，支持 30+ 宿主）、
  Cloudflare（`npx skills add` + 插件市场）都是复制文件到宿主目录，没有一家把它做成
  需要会话授权的 API。
- **双通道覆盖两类部署**：有外网的用 GitHub，纯内网的用 CP 静态路由。
- **不引入第四份真源**：skill 内容在仓库里就是最终形态，不再有「生成物」与「源」的区分。

## 后果

- `TestCatalog_SummaryPresent` 要求所有 action 都有摘要；新增 action 必须同时写摘要。
- `TestToolSpecsMatchCatalog` 要求工具引用的 action 已登记；`TestDestructiveToolsAreAnnotated`
  要求破坏性工具必须带标注且透出确认字段。
- MCP 客户端若支持 `annotations`，可在 UI 层提示破坏性操作；不支持的客户端行为不变。
- CP 需新增一个匿名静态路由；`skill/` 包被 CP 导入用于 `go:embed`。
- `apps/jmagent`（将重构为 `jm`）保持零内部包依赖，不读取 catalog。

## 关系

- 不推翻 ADR-076「策略真源唯一」与 ADR-080「capability 分组」；
  本 ADR 只补上风险标注与知识分发两处缺口。
- `agentHardDeny` 中 `instance.delete` / `instance.kill` / `node.delete` 三条字面量
  与真实 action 名（`agent.instance_delete` 等）不匹配，且在 `Authorize` 路径上未生效。
  该问题不在本 ADR 范围内改动——是否恢复为硬拒绝面需单独决策，避免在无产品判断时
  悄悄关闭 MCP 上已存在的破坏性工具。
