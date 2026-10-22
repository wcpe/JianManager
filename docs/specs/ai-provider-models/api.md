# API 规格：AI 供应商与模型（FR-514）

> 状态：**实现中**——已实现端点与本文 §2～§5 逐条对应；§6 的探测/测试端点、
> §7 的私网放行设置、§10 的审计事件**均未实现**，逐处标「未实现」。
> 关联 PRD：FR-514　·　设计见 [spec.md](spec.md)
> 依赖共享契约：[../ai-assistant/shared-contracts.md](../ai-assistant/shared-contracts.md)（本文只定义配置面，不定义会话/事件/工具契约）。
> 示例地址一律为文档保留段（`example.com`、`192.0.2.0/24`）；示例凭据均为占位，不得写入真实值。

## 1. 通用约定

- **认证**：全部端点要求登录 JWT；缺失/失效 → `401`。
- **管理面**：`/api/v1/assistant/admin/*` 的唯一判据是 `UserAccess.IsPlatformAdmin == true`
  （显式字段，**不经** `node.manage` / `requireNodes` helper）。否则 `403 {"error":"FORBIDDEN", ...}`。
  > 之所以强调：项目里有个 `requirePlatformAdmin(c)` 名字看着像平台管理员，实际只校验 `node.manage`
  > 权限节点。用它守本处会让任何持该节点的运维改写模型端点，而改端点即可把全部对话内容导向自建服务。
- **普通面**：`GET /api/v1/assistant/models` 所有登录用户可用，返回内容按 §2 裁剪。
- **JSON 命名**：camelCase；时间 RFC3339 字符串。
- **乐观并发**：写操作必须带 `version`（创建除外），不等于当前值 → `409`。
  `version` 不参与 `response` 之外的语义——它是「你改的是你看过的那个版本」的凭证。

### 1.1 错误体形态（**实现偏差**）

实际错误体只有两个字段，**没有** `code` 稳定机器码：

```json
{ "error": "CONFLICT", "message": "该供应商下仍有 3 个模型，请先删除模型" }
```

- `error` 取值：`INVALID_REQUEST` / `NOT_FOUND` / `CONFLICT` / `INVALID_CONFIG` /
  `ENCRYPTOR_UNAVAILABLE` / `IN_USE` / `FORBIDDEN` / `INTERNAL_ERROR` / `UNAUTHORIZED`。
- `message` 面向人、中文、可改文案，**永不包含凭据**。
- **为什么没有 `code`**：初版设计设想了一套细粒度机器码（`provider_has_models`、`model_is_default`…），
  落地时按「同一 HTTP 状态 + 一句人能读的原因」即可满足前端的全部分支需求——
  前端对这些错误的处理方式一致（弹提示、刷新列表），没有按机器码做差异化逻辑，
  加一层码表只会多一份要维护的映射。**若将来需要按码分支，再加 `code` 字段是加性变更**。

### 1.2 凭据永不回显

任何响应都不含密文、明文，也不含 `credentialEnv` 之外的凭据细节；
供应商视图只有布尔 `hasCredential`（是否已配置），`credentialEnv` 回显的是**变量名**（名不是秘密）。

## 2. 普通用户：可用模型摘要

### GET /api/v1/assistant/models — FR-514

请求：无参数。

```json
{
  "items": [
    { "id": 12, "modelId": "vendor-model-a", "displayName": "通用对话",
      "protocol": "openai-chat", "streaming": true, "tools": true }
  ],
  "defaultModelId": 12
}
```

- 仅返回 `provider.enabled && model.enabled` 的模型（spec §4.4）。
- `defaultModelId` 为 `number` 或 `null`；指向不可用模型（模型或其供应商停用）时为 `null`
  （**不在读路径改库**——停用可恢复，改库会把默认选择永久丢掉）。
- **禁止**出现在此响应中的字段：`baseUrl`、`credentialMode`、`credentialEnv`、`hasCredential`、`version`。
- **实现偏差**：不含 `providerName` 与嵌套的 `capabilities` 对象——前者会让摘要暴露供应商组织结构
  （普通用户不需要知道有几个供应商），后者落地为平铺 `streaming` / `tools` 两个布尔
  （与模型管理面字段一致，前端同一套渲染逻辑）。
- 空列表是合法结果（未配置），不是错误；前端据此显示「未配置 + 去配置」。
- 权限：任意登录用户。错误：401。

## 3. 管理面：供应商

### 3.1 GET /api/v1/assistant/admin/providers — FR-514

```json
{ "items": [ {
  "id": 3, "name": "默认供应商", "protocol": "anthropic",
  "baseUrl": "https://api.example.com",
  "credentialMode": "stored", "hasCredential": true,
  "enabled": true, "version": 7,
  "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-02T00:00:00Z"
} ] }
```

- `credentialEnv` 仅在 `credentialMode=env` 时出现（`omitempty`），且是**变量名**不是值。
- **实现偏差**：不含 `modelCount`。前端要显示「该供应商下有 N 个模型」时从模型列表自行归并——
  加一个只在删除时才有意义的计数，会让每次列表查询都多一次聚合。

### 3.2 POST /api/v1/assistant/admin/providers — FR-514

```json
{
  "name": "默认供应商",
  "protocol": "openai-chat",
  "baseUrl": "https://api.example.com",
  "credentialMode": "stored",
  "credentialSecret": "<占位明文>",
  "enabled": true
}
```

- 成功 `201`，返回 3.1 的单条结构。
- `credentialMode` 必填（`none` / `env` / `stored`）；`stored` 时 `credentialSecret` 必填。
- `enabled` 缺省 `true`。
- `409`：`name` 重复。
- `422 ENCRYPTOR_UNAVAILABLE`：未配置主密钥却提交 `stored` 凭据（见 §8 说明）。

### 3.3 GET /api/v1/assistant/admin/providers/:id — FR-514

返回 3.1 单条；不存在 → `404`。

### 3.4 PUT /api/v1/assistant/admin/providers/:id — FR-514

可编辑 `name`、`protocol`、`baseUrl`、`credentialMode`、凭据、`enabled`，并须带 `version`。

**凭据三态**（不传即不变）：

| 传法 | 效果 |
|---|---|
| `credentialSecret` 缺省或空串 | **保留**原密文（改显示名不会丢密钥） |
| `credentialSecret` 非空 | 替换为新密文 |
| `clearCredential: true` | 清除，且 `credentialMode` 自动降为 `none` |

- `200` 返回 3.1 单条；`409` version 不匹配；`404` 不存在。
- 改 `protocol` 或 `baseUrl` **不**改动其子模型，也不改库中的默认指针；
  但会让「默认模型是否可用」在读取侧重新评估。
- **清空不留悬空态**：不允许出现「`mode=stored` 但密文为空」——那会表现为「看起来配了密钥，
  调用时才 401」。因此清除必须同时把模式降为 `none`。

### 3.5 DELETE /api/v1/assistant/admin/providers/:id — FR-514

- `200 {"message":"已删除"}`，硬删除（spec §4.4）。
- 该供应商下仍有模型 → `409 IN_USE`（message 附模型数，要求先删模型）。
- **不级联**：级联会让一批模型在没有任何提示的情况下消失，而管理员通常只是想停掉一个供应商
  （停用用 `enabled=false`，模型原样保留）。

### 3.6 POST /api/v1/assistant/admin/providers/:id/test — **未实现**

设计意图（spec §8）：显式探测，`200 {"ok":true,"latencyMs":412,"modelCount":24}`。
当前没有该端点，**不要据本文预期它存在**。

### 3.7 GET /api/v1/assistant/admin/providers/:id/models — **未实现**

设计意图：调上游模型列表回填候选。当前没有该端点；管理员需手工填 `modelId`（**原样填入上游标识**）。

## 4. 管理面：模型与设置

### 4.1 GET /api/v1/assistant/admin/models — FR-514

```json
{ "items": [ {
  "id": 12, "providerId": 3, "providerName": "默认供应商",
  "protocol": "openai-chat", "providerEnabled": true,
  "modelId": "vendor-model-a", "displayName": "通用对话",
  "streaming": true, "tools": true,
  "contextWindow": 128000, "maxOutputTokens": 4096,
  "enabled": true, "version": 2
} ] }
```

- **无分页、无 `providerId` 过滤参数**：模型是「运维手工登记」的规模（个位数到几十），
  一次全量返回比引入分页更简单。若某部署真到数百条，再加参数是加性变更。
- `providerName` / `protocol` / `providerEnabled` 由后端**回填**，前端不必为显示一个名字再拉供应商列表。
- `contextWindow` / `maxOutputTokens` 为 `null` 表示**未声明**（不是 0）。

### 4.2 POST /api/v1/assistant/admin/models — FR-514

```json
{
  "providerId": 3,
  "modelId": "vendor-model-a",
  "displayName": "通用对话",
  "streaming": true,
  "tools": false,
  "contextWindow": 128000,
  "maxOutputTokens": 4096,
  "enabled": true
}
```

- `201` 返回 4.1 单条。
- `409`：同一供应商下 `modelId` 重复。**跨供应商同名是允许的**（不同网关提供同名模型是常态）。
- `422`：`providerId` 不存在，或参数不合规。
- **备注**：`(providerId, modelId)` 的复合唯一索引在 GORM 里要求 `ProviderID` 字段的
  `uniqueIndex` 标签名与 `ModelID` 的**完全一致**，否则两列会各自成索引，
  退化成 `modelId` 全局唯一——这个坑已用测试钉住。

### 4.3 PUT /api/v1/assistant/admin/models/:id — FR-514

可编辑 `displayName`、`enabled`、`streaming`、`tools`、`contextWindow`、`maxOutputTokens`，须带 `version`。
**不允许**改 `providerId` 与 `modelId`（要改即删后重建——改了等于换了另一个模型，
历史会话里的快照就对不上了）。

- **停用当前默认模型不清指针**（**实现偏差**，与原设计相反，理由见 spec §4.4）：
  停用可原地恢复，清指针会让「暂时关一下」永久丢掉默认选择。可用性由读取侧兜底。
- `enabled=true` 而供应商 `enabled=false` 是合法的（停用供应商不重写子模型）。

### 4.4 DELETE /api/v1/assistant/admin/models/:id — FR-514

- `200 {"message":"已删除"}`，硬删除。
- 该模型是当前默认 → **同事务清空默认指针**后删除，**不拒绝**。
  （**实现偏差**：原设计打算回 409 要求先换默认；落地改为「删除时顺手清指针」，
  因为删除是不可逆的，留悬空指针会让每次读摘要都要处理一种本可消除的状态。）
- 不存在 → `404`。

### 4.5 POST /api/v1/assistant/admin/models/:id/test — **未实现**

设计意图（spec §8）：单模型真实补全测试。当前没有该端点。

### 4.6 PUT /api/v1/assistant/admin/settings/default-model — FR-514

```json
{ "modelId": 12, "version": 4 }
```

清空默认：

```json
{ "modelId": null, "version": 4 }
```

- 成功 `200 {"message":"已保存"}`。
- 目标模型必须存在且**启用**，其供应商也必须**启用** → 否则 `422 INVALID_CONFIG`。
- `version` 不匹配 → `409 CONFLICT`。
- **实现偏差**：初版设计是 `GET/PUT /admin/settings` 一体化（含 `allowPrivateCidrs`）。
  落地收窄为单一动作端点，理由：设置表当时只有「默认模型」一个字段，
  为一个字段做「读整个设置对象 → 改 → 整体写回」会让前端多一次往返、多一个可写错的整体状态。
  **`GET /admin/settings` 未实现**（默认模型已由 `GET /assistant/models` 的 `defaultModelId` 提供），
  **`allowPrivateCidrs` 未实现**（见 spec §4.3 / §6.2 缺口）。

## 5. 端点汇总与权限

| 端点 | 权限判据 | 状态 |
|---|---|---|
| `GET /api/v1/assistant/models` | 任意登录用户 | ✅ |
| `GET /api/v1/assistant/admin/providers` | `IsPlatformAdmin` | ✅ |
| `POST /api/v1/assistant/admin/providers` | `IsPlatformAdmin` | ✅ |
| `GET /api/v1/assistant/admin/providers/:id` | `IsPlatformAdmin` | ✅ |
| `PUT /api/v1/assistant/admin/providers/:id` | `IsPlatformAdmin` | ✅ |
| `DELETE /api/v1/assistant/admin/providers/:id` | `IsPlatformAdmin` | ✅ |
| `GET /api/v1/assistant/admin/models` | `IsPlatformAdmin` | ✅ |
| `POST /api/v1/assistant/admin/models` | `IsPlatformAdmin` | ✅ |
| `PUT /api/v1/assistant/admin/models/:id` | `IsPlatformAdmin` | ✅ |
| `DELETE /api/v1/assistant/admin/models/:id` | `IsPlatformAdmin` | ✅ |
| `PUT /api/v1/assistant/admin/settings/default-model` | `IsPlatformAdmin` | ✅ |
| `POST .../providers/:id/test` | `IsPlatformAdmin` | ❌ 未实现 |
| `GET .../providers/:id/models` | `IsPlatformAdmin` | ❌ 未实现 |
| `POST .../models/:id/test` | `IsPlatformAdmin` | ❌ 未实现 |
| `GET/PUT .../admin/settings` | `IsPlatformAdmin` | ❌ 未实现（收窄为 default-model） |

后续 FR-515 的会话/run 端点、FR-517 的 MCP 端点**不在本文范围**，不得据本文推导其契约。
`/api/v1/mcp` 契约本批**未改动**，仍只认 `jmat_` Agent Token。

## 6. 校验一览

| 字段 | 规则 |
|---|---|
| `name` | 1..64，去空白后非空，唯一 |
| `protocol` | `openai-chat` \| `openai-responses` \| `anthropic` |
| `baseUrl` | 绝对 URL；**仅 `https`**（明文 http 直接 422）；无 userinfo/query/fragment；host 非空 |
| `credentialMode` | `none` \| `env` \| `stored` |
| `credentialEnv` | `^JIANMANAGER_ASSISTANT_[A-Z0-9_]{1,64}$`，仅 `mode=env` 必填 |
| `credentialSecret` | 仅 `mode=stored` 有意义；创建时必填，更新时缺省=保留 |
| `modelId` | 非空；**原样保存**，不做品牌归一化 |
| `displayName` | ≤128 字符 |
| `streaming` / `tools` | bool，缺省 `streaming=true` / `tools=false` |
| `contextWindow` / `maxOutputTokens` | 可空（`null`=未声明，**不是 0**） |
| `version` | 写操作必填，等于当前值 |

## 7. 出站失败的错误面

配置面（CRUD）**不会**发起上游请求，因此本批不存在「调用模型失败」的 HTTP 错误码语义。
协议适配层定义了统一的错误分类（`upstream_auth` / `upstream_rate_limited` /
`upstream_rejected` / `upstream_error` / `upstream_timeout` / `upstream_unreachable` /
`upstream_stream_truncated` / `egress_denied` / `egress_proxy_unsupported` /
`capability_unsupported` / `tool_call_invalid`），但它们目前**只作为内部错误类型存在**，
由未来的 FR-515 运行循环消费；本批没有任何端点把它们投影成 HTTP 响应。

## 8. 凭据与加密器

- 凭据字段是**平铺的两个字段**（`credentialSecret` / `clearCredential`），**不是**嵌套动作对象。
  （**实现偏差**：初版设计是 `{"action":"replace","value":"..."}`；平铺避免了「`action` 拼错
  被当成保留而静默不生效」这类失败模式。）
- `credentialSecret` 只出现在**请求体**；响应、日志一律不含。
- **`nil` 加密器必须显式拦截**：`KeyEncryptor.Encrypt` 在 receive 为 `nil` 时返回**空串且不报错**
  （既有降级语义）。若不拦，`stored` 凭据会以「已配置」的形态存成空密文，
  直到第一次调模型才以 401 暴露，届时现场无线索。故在路由层显式判 `nil` 并回
  `422 ENCRYPTOR_UNAVAILABLE`。
- 主密钥复用既有那套（`JIANMANAGER_CLIENT_KEY_ENC_SECRET` → 持久化 autogen → dev 回退），
  **不新增第二把密钥**。

## 9. 审计事件 — **未实现**

初版设计列了 `assistant.provider.create` / `.update` / `.delete` / `.test` 等事件。
**当前代码不写审计流水**：供应商/模型的增删改（尤其「改 baseUrl」与「清空凭据」）不留痕迹。
这与平台其它管理动作的惯例不一致，属**已知缺口**。补齐时须注意只记元数据，
绝不把 `credentialSecret` 或密文写进 detail。

## 10. 待审核点

1. `code` 机器码是否要补（当前只有 `error` + `message`，见 §1.1）。
2. 探测/测试三个端点是否仍在 FR-514 范围内补齐，还是并入 FR-515 的「首次真机联调」一起做。
3. `GET .../providers/:id/models`（上游模型回填）价值是否足够——管理员手工填 `modelId` 已可用。
4. 是否补审计流水（§9）。
5. `allowPrivateCidrs` 放开内网模型网关的入口形态（spec §4.3 缺口）。
