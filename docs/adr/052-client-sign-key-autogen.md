# ADR-052: OTA 签名密钥自动生成 + 持久化（修订 ADR-038、细化 ADR-022 信任根供给）

- **日期**: 2026-07-01
- **状态**: accepted
- **修订**: [ADR-038](038-signer-missing-degraded-startup.md) 的「生产态未注入私钥 → **降级启动**」→ 改为「未注入 → **自动生成并持久化密钥**、启用签名」。ADR-038 由本 ADR **superseded**。
- **细化**: [ADR-022](022-client-manifest-trust-and-public-endpoint.md) 决策 2/8 的**信任根私钥供给方式**（原「私钥服务端持有、env 注入不入库」补充为「未注入时由服务端自动生成并持久化到数据根」）。ADR-022 的 8 条核心决策与 `accepted` 状态不变。

## 上下文

客户端 OTA 的信任根 = manifest 的 Ed25519 签名（[ADR-022](022-client-manifest-trust-and-public-endpoint.md) 决策 2）：服务端持私钥签名、客户端 updater-core 内置**公钥**验签。签名私钥来源（`service.ResolveManifestSigner`）此前为：

- env `JIANMANAGER_CLIENT_SIGN_PRIVKEY` 注入 → 用之；
- `dev_mode=true` 未注入 → 回退源码内置开发密钥（公钥已回填 updater-core）；
- **生产未注入 → `ErrSignKeyRequiredInProd`，`StartableWithoutSigner=true` → 降级启动**（签名器 nil、OTA 不可用），见 [ADR-038](038-signer-missing-degraded-startup.md)。

真机短板：想启用 OTA 的运营者被迫**手动生成 Ed25519 密钥对 + env 注入私钥 + 把公钥回填客户端**——门槛高、易错，且降级态下 OTA 静默不可用。而 [ADR-044](044-pull-key-reversible-encryption.md) 已确立更好的自处理范式（拉取密钥加密器未配时优雅自处理，不把负担甩给运维）。

## 决策

1. **签名私钥来源改三轨**（优先级由高到低，`service.ResolveManifestSignerWithAutogen`）：
   1. **env 注入**（`JIANMANAGER_CLIENT_SIGN_PRIVKEY` 非空）：解析用之，来源 `env`（既有 `ResolveManifestSigner` 逻辑，含生产拒源码开发密钥 `ErrDevSignKeyInProd` 防线**保留**）。注入态**不生成、不持久化**。
   2. **生产未注入**（`dev_mode=false` 且未注入）：**自动生成 Ed25519 + 持久化**到 `<dataRoot>/etc/client-sign-key.pem`（PKCS#8 PEM，文件权限 `0600`，先写同目录临时文件再原子 rename 防半写）；已存在则**加载**（跨重启用同一密钥，公钥不变），来源 `generated`。
   3. **开发未注入**（`dev_mode=true` 且未注入）：**保持**回退源码内置开发密钥，来源 `dev`——其公钥已回填 updater-core，保开发端到端验签，**不改**。

2. **生成/持久化失败 → fail-fast**：信任根必须可用，生成或写盘失败时 CP `log.Fatalf` 退出（与「配错快失败」一致），不静默降级掩盖问题。注入私钥非法（`ErrInvalidSignKey`）/ 误用源码开发密钥（`ErrDevSignKeyInProd`）同样 fail-fast。

3. **面板展示公钥**：新增 `GET /client-dist/sign-key`（平台管理员）→ `{publicKey:"<SPKI base64>", keyId, source:"env"|"generated"|"dev"}`。前端「签名公钥」卡片展示公钥（等宽 + 复制）、keyId、来源徽章，并说明「把此公钥填入客户端 updater-core 的信任公钥；密钥已由服务器自动生成并持久化，勿手动删除 `etc/client-sign-key.pem`」。**只暴露公钥**——私钥绝不出服务端。

4. **首次生成打日志**：`source=generated` 首次生成时 `slog.Info` 打出公钥 + 提示运营者配入客户端 updater-core。

5. **`ErrSignKeyRequiredInProd` / `StartableWithoutSigner` 语义变更**：新装配路径（`ResolveManifestSignerWithAutogen`）下「生产未注入」不再返回 `ErrSignKeyRequiredInProd`（改为自动生成），故 `StartableWithoutSigner` 在新路径**不再命中**。二者符号**保留**供既有 `ResolveManifestSigner` 及其测试/兼容使用，代码注释标注语义变更。

## 安全论证（信任根不削弱）

自动生成**不削弱** ADR-022 的信任根，理由：

- **密钥每部署独立**：每个 CP 部署首次启动各自 `ed25519.GenerateKey` 一把独立私钥，非共享、非源码固化——与 ADR-022「私钥服务端持有」一致，只是把「运维手动生成」自动化。
- **私钥永不出服务端**：私钥仅落 `<dataRoot>/etc/client-sign-key.pem`（`0600`），端点只暴露**公钥**；公钥本就随 updater-core 公开（客户端内置验签），展示无损。
- **生产拒源码开发密钥防线保留**：显式注入源码公开的开发密钥仍被 `ErrDevSignKeyInProd` 拒绝（按解出公钥识别，防再编码绕过）；自动生成路径产出的必是**新随机密钥**（断言其公钥 ≠ 源码开发公钥）。
- **攻击者拿公钥无法伪造**：Ed25519 公钥公开不泄露签名能力；防投毒仍全靠私钥签名（ADR-022 决策 2，不动）。
- **对比 ADR-044**：拉取密钥半公开、非信任根，未配时降级为「不可查看」可接受；签名私钥是信任根，故本 ADR 选择**自动生成启用**而非降级——既消除运维门槛，又保证 OTA 从第一天可用且可信。

## 后果

- 生产态首次部署**未注入私钥即自动启用 OTA 签名**（不再降级不可用）；env 注入仍优先，注入态不生成。
- **原「生产未注入=降级」的部署升级后自动生成密钥启用签名**——属增强、无破坏（原本 OTA 就没启用），公钥经面板/日志可见。
- **密钥文件误删/丢失风险**：删 `etc/client-sign-key.pem` → 下次启动生成**新**密钥 → 公钥变、已分发客户端验签失败。面板卡片明确警告勿删；随数据根整体备份即可（ADR-010 便携运行时本就整根拷走）。
- **Windows 文件权限**：`0600` 在 Windows 语义有限，`os.WriteFile(...,0600)` 仍为惯例写法（与项目既有一致），不额外做 ACL。
- 代码：`internal/controlplane/service/client_sign_keys.go` 新增 `LoadOrGenerateSigner` + `ResolveManifestSignerWithAutogen` + 来源常量；`cmd/control-plane/main.go` 装配改调新解析器 + 生成态日志；新增 `internal/controlplane/router/client_sign_key.go`（`GET /client-dist/sign-key`）；前端签名公钥卡片。
- 文档：`docs/API.md` 增该端点；`docs/ARCHITECTURE.md` 记数据根 `etc/client-sign-key.pem` + 签名密钥来源三轨；ADR-038 标 `superseded-by ADR-052`。

## 替代方案

- **维持「生产未注入=降级」**（ADR-038 原状）— 让想用 OTA 的运营者承担手动生成 + 注入 + 回填的高门槛，且降级态 OTA 静默不可用，否决。
- **未注入时 fall-open 回退源码开发密钥签名** — 击穿信任根（ADR-022 补充已否决的 fail-open），否决。
- **自动生成但不持久化（内存态）** — 每次重启换新密钥 → 已分发客户端集体验签失败，等于灾难，否决；必须持久化到数据根跨重启稳定。
- **引入开关 `client_dist.sign_autogen` 显式启停自动生成** — 增配置面；「未注入私钥」本身已是充分的「请自动生成」信号（信任根必须可用），YAGNI，暂不引入。
