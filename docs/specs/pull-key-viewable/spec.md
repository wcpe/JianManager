# 功能规格：拉取密钥可查看（可逆加密存储）

> 状态：待审　·　关联 PRD：FR-192（增强 FR-086）　·　关联 ADR：ADR-044（本 FR 创建，修订 ADR-022 决策①）　·　分支：feature/fr-192-pull-key-viewable

## 1. 背景与目标

`ClientPullKey`（`internal/controlplane/model/client_channel.go`）当前**只存 `KeyHash`（SHA-256）+ `KeyPrefix`**，明文仅创建/轮换时一次性返回、不可二次读取（ADR-022 决策①）。但客户端整合包**一次性发出后，所有更新都依赖这把密钥**：运营若忘记密钥（只在创建时见过一次），无法为新玩家重建 `jm-updater.json`，而轮换又会让**已分发的老客户端全部失效**——等于已分发玩家集体断更。

**关键**：ADR-022 决策①白纸黑字「拉取密钥**半公开**（随整包分发必泄露），不作内容可信依据，防篡改全靠 manifest 签名」。既然 key 本就半公开，「只存哈希」没换来多少安全、却造成上述运营灾难。故让密钥**可查看**与其真实信任级别一致。

**目标**：拉取密钥改**可逆加密存储**，管理员可在频道页查看明文 + 复制。P1。修订 ADR-022 决策①的「只存哈希」。

## 2. 需求（要什么）

### 范围内
- **可逆加密存储**：`ClientPullKey` 加密文列（如 `KeyEnc`），新建/轮换密钥时同时写 `KeyHash`（**鉴权不变**）+ `KeyEnc`（明文经 AES-256-GCM 加密，密钥取自 env）。
- **查看端点**：`GET /client-channels/:id/keys/:keyId/reveal`（**仅平台管理员** + 审计 `client_key.reveal`）→ 解密返回明文。
- **前端**：密钥 tab 每行加「查看」→ 弹明文 + 复制（走 `copyToClipboard`，兼容 HTTP 非安全上下文）。
- **迁移边界**：仅新建/轮换的密钥有 `KeyEnc` 可查看；**存量「只有哈希」的老密钥不可查**（哈希单向，救不回）——前端对无 `KeyEnc` 的密钥「查看」禁用 + 提示「此密钥创建于可查看功能之前，不可找回；如需可查看请轮换（注意轮换会使已分发客户端失效）」。
- ADR-044：修订 ADR-022 决策①——「落库只存哈希」改为「哈希用于鉴权 + 可逆加密副本供管理员查看」，理由：key 半公开、防篡改靠签名，可查看与信任级一致；私钥/签名信任根**不受影响**。

### 不做（范围外）
- 改鉴权（仍用 `KeyHash` 比对）。
- 找回存量老哈希密钥（不可能）。
- 改 manifest 签名信任模型（ADR-022 决策②/⑦ 不动）。

## 3. 设计（怎么做）

### 3.1 ADR-044（本 FR 创建）
「拉取密钥可逆加密存储 + 管理员可查看」决策：为何安全级不降（key 半公开、签名才是信任根）、加密方案（AES-256-GCM、env 注入密钥、随机 nonce）、迁移边界（老密钥不可查）、审计。决策正文写 ADR，勿在 spec 重复。**ADR 文件名/编号用 `ADR-044`（主控预留，写死，勿 max+1）。** ADR 末尾引用并修订 ADR-022 决策①（ADR-022 状态仍 accepted、加「实施修订」段或由 ADR-044 supersede 决策①的存储部分）。

### 3.2 存储与加密（`model` + `service`）
- `ClientPullKey` 加 `KeyEnc string`（密文，base64 / blob；含 nonce）。迁移加性、默认空。
- 加密密钥来源：env `JIANMANAGER_CLIENT_KEY_ENC_SECRET`（32 字节 / base64），同构签名私钥的注入惯例（不入库）。
  - **未配置时**：dev（`dev_mode=true`）可用固定 dev 密钥回退；生产未配 → **不写 `KeyEnc`**（密钥仍可正常创建用，只是不可查看，查看端点返回「未配置加密、不可查」）——**不阻断建密钥**（避免把运维卡死，与 ADR-038 降级哲学一致）。
- 创建/轮换：生成明文 → `KeyHash`=SHA-256（鉴权）+ `KeyEnc`=AES-GCM(明文)（可配密钥时）。

### 3.3 端点与前端
- `GET /client-channels/:id/keys/:keyId/reveal`（平台管理员 + 审计）：有 `KeyEnc` → 解密返 `{ key }`；无 → 404 / 业务码 `KEY_NOT_REVEALABLE` + 文案。
- 前端密钥 tab：行操作加「查看」，弹明文 + 复制；无 `KeyEnc` 的行禁用并提示。复用现 `SecretDialog` 样式。

## 4. 任务拆分
- [ ] 写 `docs/adr/044-pull-key-reversible-encryption.md`（ADR-044，预留号写死，修订 ADR-022①）
- [ ] `ClientPullKey` 加 `KeyEnc` + 迁移；加密工具（AES-GCM，env 密钥，未配优雅降级）+ 单测
- [ ] 创建/轮换写 `KeyEnc`；`GET .../keys/:keyId/reveal`（管理员 + 审计）+ router 测试（管理员 200 / 越权 403 / 无 KeyEnc 404）
- [ ] 前端密钥 tab「查看」按钮 + 明文弹窗 + 复制 + 老密钥禁用提示 + i18n
- [ ] doc-sync：PRD FR-192「计划」→「开发中」；ARCHITECTURE ER（`client_pull_keys` 加 `key_enc`）+ ADR-044；API.md（reveal 端点）；CHANGELOG `[Unreleased]` 末尾追加
- [ ] 中文 commit（control-plane / web 拆 commit）

## 5. 验收标准
- 单测：加密 round-trip；未配 env 密钥优雅降级（不写 KeyEnc、不崩）；reveal 鉴权（管理员/越权/无 KeyEnc）。
- 后端编译 + 既有测试绿；鉴权（`KeyHash` 比对）行为不变。
- 前端 tsc/lint/build 绿。
- **【需真机，用户确认】** 新建/轮换密钥后可在频道页**查看明文 + 复制**；存量老哈希密钥「查看」禁用并提示；吊销/轮换仍正常；审计记录 reveal。

## 6. 风险 / 待定
- **env 加密密钥轮换**：若更换 `JIANMANAGER_CLIENT_KEY_ENC_SECRET`，旧 `KeyEnc` 解不开（密钥版本化为后续事项；首期固定一把，spec 注明）。
- **安全权衡**：可查看意味着 DB + env 密钥同时泄露才暴露 key；而 key 本就半公开（随包分发），净风险低。审计 reveal 以便追溯。
- **与 FR-191 并行**：FR-192 改 ClientChannelsPage **密钥 tab** + 后端；FR-191 改 ClientVersionsPanel **发布向导**——同页不同 tab/组件，低冲突。
