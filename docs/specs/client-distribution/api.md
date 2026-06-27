# 客户端分发 — API Spec（FR-086：频道 + 拉取密钥）

> **FR-086 产出**。分发频道（channel）与拉取密钥（pull key）的**运营管理端点**（运营者浏览器 JWT 入口）。
> 面向玩家公网的 manifest / 制品端点见 FR-087（消费本 FR 的密钥鉴权机制）。
> 关联 ADR-022（密钥半公开、只存 SHA-256 哈希、明文仅创建/轮换时一次性返回、可吊销/轮换）、契约 `contract.md` §4/§5。
> 状态：v1（2026-06-23，随实现回写）。

## 0. 约定

- 所有端点前缀 `/api/v1`，JWT 鉴权（运营者浏览器入口），**仅平台管理员**（同 `/assets`、`/users`）。
- 错误响应统一 `{ "error": CODE, "message": "...", "details": {...}? }`（见 `docs/API.md` 错误码表）。
- 创建/吊销/轮换写审计（FR-015）；**审计 detail 绝不含密钥明文**。
- 频道 `id` 为运营者指定的 slug（每服一个，作为 manifest/制品端点路径段与对外标识），非自增主键。

## 1. 数据模型（落库）

### ClientChannel（client_channels）
| 字段 | 类型 | 说明 |
|---|---|---|
| id | uint PK | 自增主键 |
| channel_id | varchar(64) uniqueIndex not null | 频道 slug（对外标识、URL 段），`^[a-z0-9][a-z0-9-]{1,63}$` |
| name | varchar(128) not null | 展示名 |
| description | varchar(512) | 描述 |
| current_version | int default 0 | **当前 latest 版本指针占位**（FR-088 编排，本 FR 仅建字段，默认 0=未发布） |
| created_at / updated_at | datetime | |
| deleted_at | soft delete | |

### ClientPullKey（client_pull_keys）
| 字段 | 类型 | 说明 |
|---|---|---|
| id | uint PK | 自增主键 |
| channel_id | varchar(64) index not null | 所属频道 slug（外键语义，随频道删除级联清理） |
| name | varchar(128) not null | 密钥名（便于识别用途，如「正式包」「灰度」） |
| key_hash | char(64) uniqueIndex not null | 拉取密钥明文的 **SHA-256 十六进制小写**；**库内不存明文** |
| key_prefix | varchar(16) not null | 明文前若干位（如 `jmck_ab12`），仅用于列表识别，不足以重建密钥 |
| revoked | bool default false index | 吊销标记；true 即鉴权失败 |
| expires_at | datetime null | 可选过期时间；到期即鉴权失败 |
| last_used_at | datetime null | 最近一次鉴权命中时间（统计用，弱一致） |
| created_at | datetime | |
| revoked_at | datetime null | 吊销时间 |

- 密钥明文格式：`jmck_` + 32 字节随机的 base64url（无填充）。明文仅在创建/轮换响应里返回一次。
- 鉴权命中以 `key_hash`（对请求头明文做 SHA-256）等值查找 + 校验 `revoked==false && (expires_at==null || now<expires_at)`。

## 2. 频道管理端点

### GET /client-channels
- **描述**: 列出全部分发频道
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200):
  ```json
  [
    { "id": 1, "channelId": "skyblock-s1", "name": "空岛一服", "description": "",
      "currentVersion": 0, "keyCount": 2, "createdAt": "datetime", "updatedAt": "datetime" }
  ]
  ```

### POST /client-channels
- **描述**: 创建分发频道（每服一个）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **请求**:
  ```json
  { "channelId": "skyblock-s1", "name": "空岛一服", "description": "可选" }
  ```
- **响应** (201): 频道对象（同列表项，`keyCount=0`）
- **错误**: 400 `INVALID_CHANNEL_ID`（slug 非法）| 400 `INVALID_REQUEST`（缺 name）| 409 `CHANNEL_EXISTS`（channelId 重复）

### GET /client-channels/:id
- **描述**: 频道详情（含密钥元数据列表，**无明文**）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **路径参数**: `:id` = 频道 slug（channelId）
- **响应** (200):
  ```json
  { "id": 1, "channelId": "skyblock-s1", "name": "空岛一服", "description": "",
    "currentVersion": 0, "createdAt": "datetime", "updatedAt": "datetime",
    "keys": [
      { "id": 10, "name": "正式包", "keyPrefix": "jmck_ab12", "revoked": false,
        "expiresAt": null, "lastUsedAt": null, "createdAt": "datetime" }
    ] }
  ```
- **错误**: 404 `CHANNEL_NOT_FOUND`

### PUT /client-channels/:id
- **描述**: 更新频道展示名/描述（channelId 不可改）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **请求**: `{ "name": "新名", "description": "新描述" }`
- **响应** (200): 频道对象
- **错误**: 404 `CHANNEL_NOT_FOUND` | 400 `INVALID_REQUEST`

### DELETE /client-channels/:id
- **描述**: 删除频道及其全部拉取密钥
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200): `{ "message": "已删除" }`
- **错误**: 404 `CHANNEL_NOT_FOUND`
- **审计**: `client_channel.delete`

## 3. 拉取密钥管理端点

### GET /client-channels/:id/keys
- **描述**: 列出频道下拉取密钥（**仅元数据，无明文**）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200):
  ```json
  [ { "id": 10, "name": "正式包", "keyPrefix": "jmck_ab12", "revoked": false,
      "expiresAt": null, "lastUsedAt": null, "createdAt": "datetime" } ]
  ```
- **错误**: 404 `CHANNEL_NOT_FOUND`

### POST /client-channels/:id/keys
- **描述**: 创建拉取密钥；**明文仅此响应返回一次，不可二次读取**
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **请求**:
  ```json
  { "name": "正式包", "expiresAt": "2027-01-01T00:00:00Z" }
  ```
  - `expiresAt` 可选（省略=永不过期）
- **响应** (201):
  ```json
  { "id": 10, "name": "正式包", "keyPrefix": "jmck_ab12", "revoked": false,
    "expiresAt": null, "createdAt": "datetime",
    "key": "jmck_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" }
  ```
  - `key` 为**一次性明文**，前端创建后立即提示用户复制保存；后续列表/详情不再返回。
- **错误**: 404 `CHANNEL_NOT_FOUND` | 400 `INVALID_REQUEST`（缺 name 或 expiresAt 格式错）
- **审计**: `client_key.create`（detail 含 channelId/name/keyPrefix，**不含明文**）

### POST /client-channels/:id/keys/:kid/rotate
- **描述**: 轮换密钥——生成新明文并替换哈希，旧明文立即失效；**新明文仅此响应返回一次**
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200): 同创建响应（含一次性 `key`）
- **错误**: 404 `CHANNEL_NOT_FOUND` / `KEY_NOT_FOUND`
- **审计**: `client_key.rotate`（detail 不含明文）

### DELETE /client-channels/:id/keys/:kid
- **描述**: 吊销密钥（保留记录、标记 revoked，立即鉴权失效）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200): `{ "message": "已吊销" }`
- **错误**: 404 `CHANNEL_NOT_FOUND` / `KEY_NOT_FOUND`
- **审计**: `client_key.revoke`（detail 含 channelId/kid/keyPrefix）

## 4. 密钥鉴权机制（供 FR-087 公网端点消费）

> 本 FR 提供 service 层校验能力 `VerifyKey`，FR-087 在面向玩家的 manifest/制品端点接入。本 FR 不实现公网端点本身。

- 入参：频道 slug + 请求头 `X-Client-Key` 明文。
- 流程：对明文做 SHA-256 → 按 `key_hash` 查找 → 校验所属 channel 匹配、`revoked==false`、未过期 → 命中则刷新 `last_used_at`（弱一致）。
- 结果：命中返回密钥与频道；未命中/吊销/过期返回 `ErrPullKeyInvalid`（FR-087 映射 401/403）。
- **半公开**：密钥随整包分发必然泄露，仅作鉴权路由 + 吊销，不作内容可信依据（内容可信靠 manifest 签名，见 ADR-022 §2、contract §3）。

## 5. 权限要求

| 端点 | 权限节点 |
|---|---|
| 全部 `/client-channels*` 管理端点 | 平台管理员（`RolePlatformAdmin`，同 `/assets`） |

## 6. 验收映射（FR-086）

| 验收标准 | 端点/能力 |
|---|---|
| channel CRUD（id/名称/当前版本指针占位/描述） | §2 全部 |
| 密钥 创建/列出/吊销/轮换；只存哈希、明文一次性 | §3 全部 + §1 模型 |
| 密钥请求头鉴权；吊销即失效 | §4 `VerifyKey` |
| 创建/吊销/轮换写审计（明文不入 detail） | §3 各端点审计 |
| 管理台「客户端分发」页 + i18n + 主题 | 前端页（web/src/pages/ClientChannelsPage.tsx） |

## 7. 发布与消费端点（FR-087）

> manifest/制品端点契约见 `contract.md` §2/§4。**鉴权分两组、物理隔离**（关键安全设计，ADR-022/023）：
> - **发布端点**（运营操作）：`/api/v1` JWT，**仅平台管理员**（同 §2/§3 频道管理）。
> - **消费端点**（玩家）：**拉取密钥** `X-Client-Key` 鉴权（**无 JWT**），与运营浏览器入口隔离。
> 理由：拉取密钥半公开（随整包分发必然泄露，§4 半公开说明），用它鉴权「发布」=严重漏洞；内容可信靠 manifest 的 Ed25519 签名而非密钥。详见 `docs/API.md` 同名章节。

| 端点 | 鉴权 | 用途 |
|---|---|---|
| `POST /client-channels/:id/files` | **JWT 平台管理员** | 上传客户端文件制品（`type=client-file` 内容寻址去重），返回 `artifact.sha256` |
| `POST /client-channels/:id/versions` | **JWT 平台管理员** | 发布版本、服务端单调递增 `version`、切 latest 指针 |
| `GET /client-channels/:id/manifest` | **拉取密钥 `X-Client-Key`** | 返回频道 latest 的签名 manifest（ETag=`version:keyId`、304） |
| `GET /client-artifacts/:sha256` | **拉取密钥 `X-Client-Key`**（任一有效密钥，跨频道共享） | 内容寻址下载制品（Range 断点续传、强缓存） |

- 服务层：`ClientVersionService.PublishFile/PublishVersion/BuildManifest/OpenArtifact`；`ClientChannelService.VerifyKey`（绑频道，manifest 用）/`VerifyAnyKey`（不绑频道，制品用）。
- 签名：`ManifestSigner`（Ed25519，canonical JSON 与客户端 `updater-core` `Json.canonical` 逐位对齐；HTTP 响应 JSON 与签名 canonical 同源）。私钥经 `JIANMANAGER_CLIENT_SIGN_PRIVKEY` 注入。
- **签名密钥 fail-closed（ADR-022 实施补充，2026-06-27；粒度细化见 ADR-038）**：私钥来源由 `service.ResolveManifestSigner(privKey, keyID, devMode)` 裁决，启动策略由 `service.StartableWithoutSigner(err)` 分流——生产态（`dev_mode=false`）**未注入** → **降级启动**（视为未启用 OTA，签名器置 nil）；显式注入**源码公开的内置开发密钥**（按解出公钥识别）→ **拒绝启动**；两种情况都绝不静默用开发密钥对外签名；仅 `dev_mode=true` 零配置回退内置开发密钥。**验收**：`dev_mode=false` + 无 `JIANMANAGER_CLIENT_SIGN_PRIVKEY` 时进程**正常启动**，但发布版本与 `GET .../manifest` 因无可用签名器返回「签名私钥未配置」（manifest 在响应时实时签名）；注入源码公开的开发密钥时进程**拒绝启动**。单测 `TestResolveManifestSigner_*` 覆盖密钥裁决（缺私钥 / 注入开发密钥 / 真私钥 / 开发回退）；降级启动（未注入时进程不退出）由 `StartableWithoutSigner` 判定、以实机重启验证。
- 发布写审计：`client_file.publish` / `client_version.publish`。
