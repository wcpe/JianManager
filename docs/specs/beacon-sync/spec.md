# JianManager 与 Beacon 协同（FR-443 / FR-444）

> 状态：开发中·真机验收通过（待发版）　·　关联 PRD：FR-443、FR-444　·　依赖：ADR-090、Beacon FR-222

## 1. 背景

JianManager（实例生命周期管理）与 Beacon（区服配置中心）是**两套独立系统**，当前零集成：JianManager 全仓库仅 2 处 ADR 引用 Beacon，无实际调用代码。

实测代价：一次 60 台实例的拓扑搭建，需要**人工双写**——在 JianManager 改实例名/归属，再到 Beacon 管理台逐个分派小区、设默认入口。60 台 = 60 次分派 + 12 次默认入口，全程手工。

本 FR 建立**双向协同**，消除人工双写。

## 2. 核心约束：可选协同，不是依赖（关键）

**这是本 spec 最重要的设计约束。**

| 场景 | 要求行为 |
|---|---|
| 只用 JianManager，不部署 Beacon | **全部功能正常**，无任何报错或降级提示 |
| 只用 Beacon，不用 JianManager | Beacon 行为**完全不变**（agent 仍主动注册 + 人工审批） |
| 两者都部署且配置协同 | 拓扑双向同步，可选的自动化增强 |

具体到实现：
- JianManager 侧的协同端点是**可选配置**；未配置即整体跳过
- 推送失败**只写审计与告警，绝不阻塞或回滚本机操作**（决策 3A）
- 拉取在 Beacon 不可达时返回明确错误且**不改动本地任何数据**
- Beacon 侧的机器注册通道是**默认关闭**的开关（FR-222）

> 换言之：协同能力全部是**加法**，任何一侧缺席都不产生减法。

## 3. FR-443：向 Beacon 推送拓扑变更

### 3.1 触发时机（决策：仅拓扑变更）

| 事件 | 是否推送 | 理由 |
|---|---|---|
| 实例创建 | ✅ | 需在 Beacon 建立 server 记录 |
| 实例删除 | ✅ | 需归档 Beacon 侧记录 |
| 实例改名 | ✅ | `serverId` 语义变更，需同步 |
| 实例改归属（role/tags） | ✅ | 影响区服分派 |
| **实例启停 / 重启** | ❌ | 高频操作，且 Beacon 的在线状态由 agent 心跳自行维护，无需推送 |

> 启停不推送是**刻意决策**：批量重启 60 台会产生 120 次推送，而 Beacon 的健康状态本来就由 agent 心跳驱动，推送只会造成重复真源。

### 3.2 调用方式

JianManager 作为受信内部调用方，走 Beacon 的机器注册通道（FR-222）：

```
POST {beaconEndpoint}/beacon/v1/agent/register
Headers:
  X-Beacon-Token: {配置的共享 token}
Body: { serverId, address, kind, namespace, ... }
```

**前提**：Beacon 侧开启 `allow-machine-register`。若未开启，注册请求落 pending，JianManager 侧**视为成功投递但不生效**（并在审计中记录该状态）。

### 3.3 失败处理（决策 3A）

```
推送 → 成功？ 
  ├─ 是 → 写审计（ok）
  └─ 否 → 写审计（fail）+ 记录错误详情
          ↓
        【不重试、不回滚、不阻塞】
```

- **不重试**：本 FR 不做重试队列。理由：拓扑变更频率低，人工补做代价可接受；重试队列引入的状态机复杂度不成比例
- **不回滚**：本机操作已成功，不因外部系统不可达而撤销
- **不阻塞**：推送在异步 goroutine 中执行，不进入用户操作的响应路径

新增审计动作：`instance.beacon_push_ok` / `instance.beacon_push_fail`。

### 3.4 配置

```yaml
beacon:
  # 协同端点；留空 = 不启用协同（默认）
  endpoint: ""
  # 共享 token（须与 Beacon 的 agent-token 一致）
  token: ""
  # 推送目标 Beacon 环境的 namespace code（如 prod/test）
  # Beacon 的机器注册要据此解析归属才能落 server 行与身份，缺省即无从绑定
  namespace: ""
  # 是否启用推送
  push-enabled: false
```

**启动期校验（fail-fast）**：启用 `push-enabled` 时 `endpoint` 与 `namespace` 均为必需项，缺失在**启动期**报错拒绝启动（`ValidateBeaconConfig`，与 `ValidateJWTSecret` 同处调用）。

> **为什么不留在运行时跳过**：每次拓扑变更都产生一条 `NAMESPACE_NOT_FOUND` 或「未配 namespace」的 fail 审计是纯噪声，且运维无法从审计区分「配错了」与「没配」。明确 opt-in 就 fail-fast——未启用推送的部署零影响。
>
> **为什么不校验 `pull-enabled`**：拉取是手动触发且同步返回错误，发起人当场可见，不需要启动期拦截。

## 4. FR-444：从 Beacon 拉取拓扑

### 4.1 映射规则

```
Beacon 结构                          →  JianManager 映射
─────────────────────────────────────────────────────────
bc_cluster（如 bc-main）             →  根分组 "bc-main"
  └─ region（如 r1）                 →  子分组 "r1"
       └─ zone（如 z1）              →  子分组 "z1"
            └─ server（如 r1-z1-g1） →  实例加入该分组
```

同时为实例补标签（FR-440）：
- `region:{region.code}`
- `zone:{zone.code}`
- `role:{lobby|game}`（由 `isDefaultEntry` 推断：默认入口=lobby，其余=game）

### 4.2 手动触发（关键决策）

**首次拉取必须手动触发**，不自动建树。理由：

- 自动建树会与人工已建的分组冲突（分组可能承载了非 Beacon 语义的归类）
- 首次对齐需要运维确认映射规则（尤其是 role 推断）
- 后续可选开启定时对账（本 FR 不做，YAGNI）

### 4.3 冲突处理

| 冲突 | 处理 |
|---|---|
| 分组已存在同名节点 | 复用，不重复创建 |
| 实例已在其他分组 | **移入** Beacon 对应分组（Beacon 为拓扑真源） |
| Beacon 有而本地无对应实例 | 跳过并记录警告（不同步创建实例） |
| 本地有而 Beacon 无 | **保留不动**（本地可能有未接入 Beacon 的实例） |

### 4.4 失败处理

Beacon 不可达时：
- 返回明确错误（不静默成功）
- **不改动本地任何数据**（先全量拉取到内存，校验通过后才事务性写入）

### 4.5 配置

```yaml
beacon:
  # 复用 FR-443 的 endpoint/token
  pull-enabled: false
```

## 5. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | **未配置协同端点时，实例创建/启动/改名全部正常，无报错** | 真机 |
| 2 | 配置推送后，实例改名能在 Beacon 侧反映 | 真机（两端联动） |
| 3 | Beacon 不可达时，本机操作**照常成功**，仅有审计记录 | 真机（断开 Beacon） |
| 4 | 推送失败有审计（动作名 / 错误详情） | 管理台审计 |
| 5 | 实例启停**不产生推送**（避免风暴） | 真机（观察审计） |
| 6 | 拉取能生成分组树：集群 → 大区 → 小区 → 实例 | 真机 |
| 7 | 拉取能补 `region:` / `zone:` / `role:` 标签 | 真机 |
| 8 | Beacon 不可达时拉取**报错且本地零改动** | 真机（断开 Beacon 后对比分组树） |
| 9 | 重复拉取幂等（不产生重复分组） | 真机（连续两次拉取） |
| 10 | 未部署 Beacon 时，分组树功能独立可用 | 真机 |

### 5.1 真机验证记录（2026-09-21，隔离环境 E2E CP 19501 + Worker 19702 + mock Beacon 18090）

| # | 结果 | 证据 |
|---|---|---|
| 1 | ✅ 通过 | 未配置端点时创建/启动/改名全部正常，无报错 |
| 2 | ✅ 通过 | mock Beacon 收到 `POST /beacon/v1/agent/register`，携 `X-Beacon-Token` 与 `namespace:prod`；`create`/`ownership` 事件按变更触发，收包 metadata 中 `jmTags` 与变更逐一吻合（设标签→覆盖→清空三次变更对应三份不同 payload） |
| 3 | ✅ 通过 | 端点不可达时改名 **39ms** 完成（零阻塞），本机操作全部成功 |
| 4 | ✅ 通过 | 审计落 `instance.beacon_push_ok`（正向）/ 失败审计（反向），detail 含 event/namespace/role/address |
| 5 | ✅ 通过 | 实例启停前后推送审计条数不变（2→2） |
| 6 | ✅ 通过 | 空树 → 拉取创建 4 个分组，三层层级正确落库（`bc-main` → `r1` → `z1`/`z2`），本地不存在的 server 列入 `skippedServers` |
| 7 | ✅ 通过 | 建同名实例 `r1-z1-g1` 后重跑拉取：`matchedInstances:1, retaggedInstances:1, movedInstances:1`；标签精确落库 `["region:r1","zone:z1","role:lobby"]`（role 取自 Beacon 侧角色），实例挂到完整层级路径 `bc-main`→`r1`→`z1`，无关的 `z2` 保持空成员 |
| 8 | ✅ 通过 | 不可达时返回明确错误且本地分组树零改动 |
| 9 | ✅ 通过 | 二次拉取 `createdGroups=0`，幂等 |
| 10 | ✅ 通过 | 分组树 CRUD 独立可用（不依赖 Beacon） |

## 6. 实现要点

| 位置 | 改动 |
|---|---|
| `internal/controlplane/service/beacon_sync.go`（新） | 推送与拉取服务 |
| `internal/controlplane/service/instance.go` | 创建/改名/删除后异步触发推送 |
| `internal/controlplane/service/instance_group.go` | 拉取时建树 |
| `internal/controlplane/router/`（新） | 手动触发拉取的端点 |
| `internal/controlplane/mcp/tools_instance_group.go` | 可选：MCP 触发拉取 |
| `internal/controlplane/config/` | `beacon.*` 配置段 |

## 7. 未决 / 待确认

- **推送的幂等性**：Beacon 注册端点是否幂等取决于 FR-222 的实现；JianManager 侧按「重复推送同一 serverId 应成功」假设，若 Beacon 返回冲突则记为 fail 并审计。
- **定时对账**：本 FR 不做。若后续需要，建议独立 FR（涉及差异计算与冲突策略）。
- **namespace 选择**：JianManager 若管理多个逻辑环境，需指定推送到哪个 Beacon namespace。首版用单一配置项，不做多环境映射。
