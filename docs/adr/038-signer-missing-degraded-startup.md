# ADR-038: 客户端签名私钥缺失时降级启动而非阻断（细化 ADR-022 fail-closed 粒度）

- **日期**: 2026-06-27
- **状态**: accepted
- **关系**: 细化（refines）[ADR-022](022-client-manifest-trust-and-public-endpoint.md) 的「实施补充（签名密钥 fail-closed 强化）」；不取代 ADR-022 任何核心决策。

## 上下文

[ADR-022](022-client-manifest-trust-and-public-endpoint.md) 的实施补充（2026-06-27）把客户端 OTA manifest 签名密钥裁决改为 **fail-closed**，其中规定：生产态（`dev_mode=false`）未注入 `JIANMANAGER_CLIENT_SIGN_PRIVKEY` → **拒绝整个 CP 启动**。

真机部署暴露一个可用性问题：**客户端 OTA 分发（FR-086~098）是可选功能**，多数部署只用面板与节点 / 实例管理，并不对外分发玩家客户端。但「未注入 OTA 签名私钥即拒绝启动」把一个未使用的可选功能变成了**整个 Control Plane 的启动硬依赖**——运营者被迫为根本不用的功能生成并注入 Ed25519 私钥，否则进程退出（日志：`初始化客户端分发签名器失败: 生产态（dev_mode=false）必须经 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入...`）。

## 决策

把 fail-closed 的**失败方式**按错误类型分流（`service.StartableWithoutSigner(err)`）：

1. 生产态**未注入**私钥（`ErrSignKeyRequiredInProd`）= 未启用 OTA → **降级启动**：签名器置 `nil`，CP 正常启动；客户端 OTA 的发布 / 签名 manifest / `.jmpack` 打包在**被调用时**返回 `ErrSignKeyNotConfigured`（功能不可用、可见报错），其余 CP 功能不受影响。
2. **注入了无效私钥**（`ErrInvalidSignKey`）或**误用源码公开的内置开发密钥**（`ErrDevSignKeyInProd`，按解出公钥识别）= 想用却配错 → 维持 **fail-fast 拒绝启动**，让运维即时修正。

**安全根本不变**：降级 = 「不能签」（`signer=nil` 拒绝签名），**绝不** fall-open 到源码公开的开发密钥对外签名。ADR-022 决策 2 / 8 的「私钥保密、信任根不可伪造」前提完全守住。

## 理由

- 「未配置的可选功能」与「配错的必用功能」是两类语义：前者降级、后者快失败，是良好工程默认；把二者都按「拒绝启动」处理，让未启用 OTA 的部署承担无谓负担。
- fail-closed 的本质是「绝不用弱 / 公开密钥对外签名」，而非「必须阻断进程」。降级（拒绝签名但允许启动）同样满足 fail-closed——攻击面（用源码公开的开发密钥伪造 OTA 包）依旧关闭。
- 消费服务 `ClientVersionService.BuildManifest` / `JmPackService.PackVersion` **本就为 `nil` signer 设计了拒绝路径**（返回 `ErrSignKeyNotConfigured`），降级无需新增 fall-open 风险，只是把「失败」从启动期前移到功能调用期。

## 后果

- 生产态首次部署不再被迫为未使用的 OTA 功能配置签名私钥即可启动；注入私钥后客户端 OTA 即启用，无需开关。
- 配错私钥（无效 / 误用开发密钥）仍 **fail-fast**，对运维即时可见，不会静默降级掩盖配置错误。
- ADR-022 实施补充的「未注入 → 拒绝启动」由本 ADR 细化为「未注入 → 降级」；ADR-022 的 8 条核心决策与「配错 → 拒绝启动」均不变，ADR-022 状态仍 `accepted`。
- 代码：`internal/controlplane/service/client_sign_keys.go` 新增 `StartableWithoutSigner`；`cmd/control-plane/main.go` 据此分流降级 / fatal。

## 替代方案

- **维持「未注入即拒绝启动」**（ADR-022 实施补充原状）— 让未启用 OTA 的部署承担无谓的签名配置硬依赖，否决。
- **未注入时 fall-open 回退开发密钥签名** — 击穿信任根（正是 ADR-022 实施补充否决的 fail-open），否决。
- **引入独立开关 `client_dist.enabled` 显式启停 OTA** — 增配置面；「未注入私钥」本身已是充分的「未启用」信号，YAGNI，暂不引入（未来若需显式开关再议）。
