# 功能规格：OTA 签名密钥自动生成 + 面板公钥展示

> 状态：❌ 已废弃（ADR-054 / FR-256 去掉 manifest 验签与签名密钥管理）　·　关联 PRD：FR-248（已废弃）　·　关联 ADR：ADR-052（历史记录，已被 ADR-054 推翻）　·　分支：feature/fr-248-sign-key-autogen
>
> **废弃注记（2026-07-03）**：本文保留 FR-248 的历史设计记录，不再代表现行实现；现行客户端分发信任模型见 ADR-054、`docs/ARCHITECTURE.md` 与 `docs/API.md`。

## 1. 背景与目标

客户端 OTA 的信任根 = manifest 的 Ed25519 签名（ADR-022 决策 2）：服务端持私钥签名、客户端 updater-core 内置**公钥**验签。当前私钥来源（`service.ResolveManifestSigner`，见 `client_sign_keys.go`）：env `JIANMANAGER_CLIENT_SIGN_PRIVKEY` 注入 → 用之；dev_mode 未注入 → 回退源码内置开发密钥；**生产未注入 → `ErrSignKeyRequiredInProd`，`StartableWithoutSigner`=true → 降级启动（签名器 nil、OTA 不可用）**（ADR-038）。

真机短板：想用 OTA 的运营者被迫**手动生成 Ed25519 密钥对 + env 注入私钥 + 把公钥回填客户端**才能启用——门槛高、易错。而 ADR-044 已确立更好的范式（拉取密钥加密器 `ResolveKeyEncryptor`：env 注入优先、未配优雅自处理）。

**目标**：CP 启动时**未经 env 注入私钥则自动生成 Ed25519 密钥对并持久化到数据根文件**（跨重启稳定），env 注入仍**优先**（双轨）；面板展示**公钥**（+ 来源 + 复制）供运营者配到客户端 updater-core。P1。据此**修订 ADR-038**（未注入由「降级」改「自动生成」）、细化 ADR-022 信任根供给 → **ADR-052**。

## 2. 需求（要什么）

### 范围内
- **签名私钥来源三轨**（优先级由高到低）：
  1. **env 注入**（`JIANMANAGER_CLIENT_SIGN_PRIVKEY` 非空）：解析用之，来源 `env`（既有逻辑、含生产拒用源码开发密钥 `ErrDevSignKeyInProd` 防线**保留**）。注入态**不生成、不持久化**。
  2. **生产未注入**（`dev_mode=false` 且未注入）：**自动生成 Ed25519 + 持久化**到 `<dataRoot>/etc/client-sign-key.pem`（PKCS#8 PEM，文件权限 0600）；已存在则**加载**（跨重启用同一密钥，来源 `generated`）。
  3. **开发未注入**（`dev_mode=true` 且未注入）：**保持**回退源码内置开发密钥（来源 `dev`）——其公钥已回填 updater-core，保开发端到端验签，**不改**。
- **面板公钥展示端点**：`GET /client-dist/sign-key`（JWT 平台管理员）→ `{publicKey:"<SPKI base64>", keyId, source:"env"|"generated"|"dev"}`。
- **前端公钥卡片**：客户端分发页新增「签名公钥」信息卡——展示公钥（等宽 + 复制按钮）、keyId、来源徽章，并大白话说明「把此公钥填入客户端 updater-core 的信任公钥，客户端才会信任本服务器签发的更新；密钥已由服务器自动生成并持久化，勿手动删除 `etc/client-sign-key.pem`」。
- ADR-052（修订 ADR-038、细化 ADR-022），doc-sync。

### 不做（范围外）
- 密钥**轮换**（k2…）UI / 多密钥集：本期单密钥（keyId=k1）；轮换留后续（与 ADR-022 决策 8 兼容，本 FR 不做）。
- 私钥**导出/下载**端点：私钥绝不出服务端（信任根），只暴露公钥。
- 改 manifest 签名/验签算法或 canonical 规则（`client_manifest.go` 的签名逻辑不动）。
- 拉取密钥（那是 ADR-044 的另一套，不混）。

## 3. 设计（怎么做）

### 3.1 密钥来源与持久化（`internal/controlplane/service/client_sign_keys.go`）
- 新增 `LoadOrGenerateSigner(keyPath, keyID string) (*ManifestSigner, error)`：`keyPath` 存在 → 读 PEM 解 PKCS#8 构签名器；不存在 → `ed25519.GenerateKey` → `x509.MarshalPKCS8PrivateKey` → PEM 编码 → `os.WriteFile(keyPath, pem, 0600)`（先写临时再 rename 防半写）→ 构签名器。父目录 `etc/` 由 dataroot 保证存在。
- 新增总裁决 `ResolveManifestSignerWithAutogen(privKeyB64, keyID string, devMode bool, keyPath string) (signer *ManifestSigner, source string, err error)`：
  - env 注入非空 → 走既有 `NewManifestSigner` + 生产 dev-key 防线 → `source="env"`；
  - 未注入 + devMode → 内置开发密钥 → `source="dev"`；
  - 未注入 + !devMode → `LoadOrGenerateSigner(keyPath, keyID)` → `source="generated"`。
  - 复用既有错误哨兵；生成/持久化失败**返回错误**（main.go 据此 fatal——信任根必须可用，与「配错快失败」一致）。
- `ErrSignKeyRequiredInProd` / `StartableWithoutSigner` 在新路径下**不再对「未注入」触发**（改为自动生成）；保留符号供既有测试/兼容，注释标注语义变更（见 ADR-052）。
- `ManifestSigner.PublicKeySPKIBase64()` 已存在，直接复用。

### 3.2 装配（`cmd/control-plane/main.go`）
- 现 `ResolveManifestSigner(...)` 调用点改为 `ResolveManifestSignerWithAutogen(cfg.ClientDist.SignPrivKey, cfg.ClientDist.SignKeyID, cfg.Server.DevMode, root.Abs("etc/client-sign-key.pem"))`（用 `dataroot.Root`）。
- 日志：`source=generated` 首次生成时 `slog.Info` 打出**公钥** + 提示「已自动生成 OTA 签名密钥，请将上述公钥配入客户端 updater-core」；`dev` 维持既有告警。
- 把签名器（或其公钥/来源）传给新的 `ClientSignKeyHandler`。

### 3.3 端点（新文件 `internal/controlplane/router/client_sign_key.go`，**不碰** `client_version.go`）
- `ClientSignKeyHandler{ signer *service.ManifestSigner; source string }` + `GetSignKey`（`requirePlatformAdmin`）→ 200 `{publicKey,keyId,source}`；signer 为 nil（理论上仅生成失败已 fatal，不会到）→ 503 `SIGN_KEY_NOT_CONFIGURED`。
- `RegisterRoutes(rg)`：`rg.GET("/client-dist/sign-key", h.GetSignKey)` 挂发布组（JWT 管理员）。main.go 注册。

### 3.4 前端
- `web/src/api/clientDistSignKey.ts`（或并入既有 client api）：`useClientSignKey()`（TanStack Query，平台管理员，`enabled` 门控）。
- 「签名公钥」卡片组件，挂到客户端分发落地页（`ClientChannelsPage` 顶部信息区或分发监控页设置区，择显眼处）。复制按钮复用站内既有 copy 兜底。
- `mocks/handlers/domains/client.ts`：加 `/client-dist/sign-key` MSW handler。

## 4. 任务拆分
- [ ] `client_sign_keys.go`：`LoadOrGenerateSigner` + `ResolveManifestSignerWithAutogen` + 单测（红→绿：生成后再加载得同密钥、env 优先、dev 回退、生产未注入自动生成且持久化且重载稳定、持久化失败报错、生产拒源码开发密钥仍生效）
- [ ] main.go 装配改造 + 生成态日志打公钥
- [ ] `client_sign_key.go` 端点 + handler + 路由 + router 测试
- [ ] 前端公钥卡片 + `useClientSignKey` + MSW + dom 测试
- [ ] **ADR-052**（修订 ADR-038、细化 ADR-022）；把 ADR-038 状态标 `superseded-by ADR-052`
- [ ] i18n zh/en；暗亮主题
- [ ] doc-sync：`docs/API.md`（新端点）、`docs/ARCHITECTURE.md`（数据根 `etc/client-sign-key.pem` + 签名密钥来源三轨）、PRD FR-248「计划」→「开发中」（只改本行）、CHANGELOG `[Unreleased]` 末尾追加
- [ ] 中文 commit（feat(control-plane) 后端、docs(adr) ADR、feat(web) 前端拆 commit）

## 5. 验收标准
- 后端 `go build ./...` + `go test ./internal/controlplane/...` 绿；前端 tsc/eslint/build + vitest 绿。
- 生产态（`dev_mode=false`）未注入私钥启动：**不再降级**，自动在 `etc/client-sign-key.pem`(0600) 生成密钥，OTA 可用；**重启后用同一密钥**（公钥不变）——单测/集成断言。
- env 注入私钥时：用注入密钥、**不生成/不写文件**，来源 `env`；生产注入源码开发密钥仍被拒（`ErrDevSignKeyInProd`）。
- `GET /client-dist/sign-key` 返回正确公钥/keyId/source；非管理员 403。
- 前端卡片展示公钥 + 来源 + 可复制 + 大白话说明；zh/en + 暗亮正常。
- **【需真机，用户确认】** 全新数据根生产态启动 → 面板见自动生成的公钥 → 该公钥与 `etc/client-sign-key.pem` 派生的一致 → 重启公钥不变。（真机为硬闸。）

## 6. 风险 / 待定
- **信任根安全**（ADR-052 详述）：自动生成不削弱信任根——密钥每部署独立、`0600`、私钥永不出服务端；公钥本就公开（展示无损）；生产拒源码开发密钥防线保留；攻击者拿公钥无法伪造签名。
- **密钥文件误删/丢失**：删 `etc/client-sign-key.pem` → 下次启动生成**新**密钥 → 公钥变、已分发客户端验签失败。卡片文案明确警告勿删；随数据根整体备份即可（ADR-010 便携运行时本就整根拷走）。
- **Windows 文件权限**：`0600` 在 Windows 语义有限，`os.WriteFile(...,0600)` 仍为惯例写法（与项目既有一致），不额外做 ACL。
- **既有降级用户迁移**：原「生产未注入=降级」的部署升级后将自动生成密钥启用签名——属增强、无破坏（原本 OTA 就没启用）；ADR-052 记此行为变更。
