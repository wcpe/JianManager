# ADR-080: Agent 能力策略 v2 与节点继承实例作用域

- **日期**: 2026-07-26
- **状态**: accepted
- **上下文**: FR-384/389 的 Agent Token 仅支持默认只读、两个写白名单和显式实例/节点 ID scope。FR-396~399 需要开放更多节点、实例、内容、Bot 与观测操作，同时必须保证既有 Token 升级后不扩权，并保持 Control Plane 为唯一策略真源。
- **取代**: ADR-076 中“默认只读 + 两项写白名单”作为长期授权模型，以及将实例删除、节点删除、强杀、制品管理列为永久硬拒绝面的决策；ADR-076 的专用 Agent Token、CP 策略真源、与人类 JWT/jmctl 分离、Agent 审计主体等决策继续有效。

## 决策

1. **采用版本化策略，不原地改写旧 Token。**
   - V1 Token 保留现有 `write_allowlist` 和显式 ID scope 的精确语义。
   - V2 Token 使用固定 capability 分组与显式 `policy_version=2`。
   - 旧记录保持 `policy_version=1`，不得通过数据库回填把旧白名单自动转换为更宽能力。

2. **能力分组是 Agent 授权的长期扩展单元。**
   - 节点：`node.read`、`node.operate`、`node.destructive`。
   - 实例：`instance.read`、`instance.life`、`instance.command`、`instance.provision`、`instance.configure`、`instance.content`、`instance.destructive`。
   - Bot：`bot.read`、`bot.manage`、`bot.load`。
   - 观测：`observability.read`。
   - 未知 capability 和未知 action 默认拒绝。

3. **V2 节点 scope 单向覆盖实例。**
   - 实例显式 scope 与授权节点上的当前实例构成并集。
   - 新建到授权节点的实例自动进入 scope；移出授权节点后自动失权。
   - 实例 scope 不反向授予节点读取或节点运维。
   - V1 Token 不启用该继承，避免升级扩权。

4. **service action 目录是 action→capability→scope 的唯一真源。**
   - HTTP Agent Ops、MCP、调用流水都从同一目录投影。
   - MCP 只保存工具协议定义、目标提取与 service 执行器，不复制 capability 规则。
   - jmagent 只调用 Agent HTTP，不本地解释策略。

5. **授权分为发现与执行两层。**
   - `tools/list` 按当前 Token 的 capability 与潜在可用 scope 裁剪。
   - `tools/call` 和 Agent HTTP 在可信目标解析后执行最终授权；客户端缓存或伪造工具列表不能绕过。
   - scope 外目标不得进入 Worker RPC 或业务写路径。

6. **Token 能力与 scope 不在线编辑。**
   - 调整权限继续采用吊销旧 Token、重新签发新 Token。
   - 这样 MCP 会话中的 principal 快照不会因策略热更新产生不一致；吊销仍由每次请求鉴权立即生效。

7. **永久禁区缩减为平台治理与秘密面。**
   - 用户/组/RBAC、Agent Token 管理、密钥或准入凭据明文、数据库浏览、自更新、平台设置永不进入 Agent action 目录。
   - 审计/调用流水删除继续不开放。
   - 实例删除、节点删除、强杀和制品/内容操作不在本 ADR 中自动开放；它们只有在后续 FR 以强类型 action、独立 destructive capability、精确确认、状态机和审计守卫明确登记后才可能开放。

8. **调用流水记录实际授权能力。**
   - V2 记录 action 对应 capability。
   - V1 记录 `legacy.*` 兼容标签。
   - action 与 targetType/targetId 继续保留，历史记录允许 capability 为空。

9. **不提供通用 MCP API 代理。**
   - MCP 不注册任意 method/path/body 调用工具。
   - 大型文件数据继续使用流式 HTTP 数据面，MCP 仅承担强类型控制。

## 理由

- 直接把 `write_allowlist` 重命名或映射到能力组会让旧 Token 随组内 action 扩展而意外获得新权限。
- 节点继承 scope 必须依据实例当前归属动态判断，不能签发时快照实例 ID，否则未来实例无法自动覆盖，迁移后也会残留错误权限。
- 动态 `tools/list` 改善最小暴露，但不能替代执行时授权；两层门禁才能抵抗缓存、伪造和竞态。
- 将 action 映射集中在 service 可避免 HTTP、MCP 与调用流水各自维护 switch 后发生策略漂移。
- destructive 能力与普通运维分离，允许后续开放近管理员操作而不把高危动作混入常规分组。

## 后果

- `agent_tokens` 增加 `policy_version` 与 `capabilities`；`write_allowlist` 保留用于 V1 兼容。
- `agent_call_logs` 增加可空 `capability`。
- 策略服务需要动态实例 scope 查询，并在写操作派发前重验实例归属。
- Token 管理 API/UI 同时展示策略版本、capabilities 和旧 writeAllowlist。
- MCP `tools/list` 从静态全量变为按 principal 裁剪，`tools/call` 继续最终鉴权。
- FR-396~399 必须把新 action 登记到 action 目录，不得在各自 handler/tool 内发明能力规则。

## 不变量

- Agent Token 与人类 JWT 分离，明文 Token 仅创建时返回，库内只存 hash。
- Control Plane 是 Agent 策略唯一真源。
- jmctl 仍是本机应急通道，不纳入 Agent 日常授权。
- 401 表示凭据无效/过期/吊销；403 表示策略拒绝；MCP 策略拒绝保持 HTTP 200 + `isError=true`。
- 永久禁区不注册 MCP tool，也不能通过 Agent HTTP 绕过。

## 关系

- **Supersedes**: ADR-076 的 V1 长期授权模型与旧硬拒绝边界。
- **Amends**: ADR-077；MCP 工具集改为 action 目录投影并按 Token 动态裁剪，协议层仍不复制策略。
- **Depends on**: FR-384、FR-389、FR-390。
- **Enables**: FR-396、FR-397、FR-398、FR-399。

## 附注：压测模板的所有权语义（FR-398）

`BotLoadTemplateService` 的 CRUD 以 `userID` + `isAdmin` 判断所有权，但 Agent Token 不是用户，需要显式定型：

- **Agent 的模板操作以平台级视角执行**（等价 `isAdmin=true`、`userID=0`）。持有 `bot.load` 即可管理全部压测模板，包含管理台用户创建的模板。
- 理由：Token 的授权边界已由 capability 与 scope 完整表达，再叠加一层用户所有权会产生「Agent 创建的模板归属于谁」的悖论，并使模板在人机之间不可共享——而模板本身不绑定实例，也就没有可继承的 scope 归属。
- 因此模板类 action 的 `ResourceType` 保持 `none`，只由 capability 把关，不引入新的资源类型。
- 代偿护栏：`loadtest_template_delete` 要求 `confirmTemplateName` 精确等于目标模板名称，弥补无所有权隔离带来的误删风险。
- 该决策不排除未来做隔离：若需要「Agent 专属模板命名空间」或按 Token 隔离可见性，另开 FR 处理，届时本附注被替换而非默默漂移。
