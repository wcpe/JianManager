# 功能规格：日志采集规则引擎（FR-502）

> 状态：**已实现 + 真机验收通过**（2026-10-15）
> 关联 PRD：**FR-502**　·　依赖：FR-474（Worker 日志采集与持久化通道）
> 关联：`docs/specs/worker-log-acquisition/spec.md`（采集底座，本项为其路径发现机制的增强）
> 动因：2026-10-15 生产实测——`bungee-main` 代理日志在平台**完全缺失**（VL 0 条），而 Paper 服正常（2465 条/24h）
>
> **实现落点**：`internal/worker/logs/ingest/rules.go`（规则表）+ `instances.go`（路径解析与源重置）
> + `internal/controlplane/service/log_rule_derive.go`（CP 侧推导）+ `proto/worker.proto` 字段 25/26。
>
> **真机验收（2026-10-15）**：`bungee-main` 的 `proxy.log.0` 已被采集并可在 VL 查询
> （此前长期 0 条）；端到端验证——向日志追加标记行后位置推进 80 字节且该行入库。
>
> **实现中发现并修复的三个缺口**（原 spec 未覆盖，已回写本文档）：
> 1. **持久化遗漏**：`rule_key`/`relative_path` 未随 binding 落库 ⇒ 重启后规则丢失、
>    BungeeCord 采集退回错误路径。已补 `InstanceRow` 两列 + 往返读写 + 存量库 `addColumnIfMissing` 迁移。
> 2. **规则升级被误判为绑定变更**：既有实例首次引入规则时，旧绑定（无规则）与新绑定不等，
>    被「绑定不得变更」保护拒绝 ⇒ 规则永远无法在存量实例生效。已按「源位置是否变化」区分处理。
> 3. **重读导致重复导入**：源重建后从头读文件，把已投递过的历史内容再投一遍（真机实测 30 倍重复）。
>    已改为锚定 EOF 起算（沿用既有 `reanchorAtEOFLocked` 的语义）。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态与覆盖语义以
`docs/specs/worker-log-platform-contract/spec.md` 为准。

---

## 1. 背景与目标

**问题**：实例活动日志路径被硬编码，导致非 Paper 核心（尤其 BungeeCord）的日志无法采集。

**精确根因**（2026-10-15 调查，`internal/worker/logs/ingest/instances.go:154`）：

```go
for _, stream := range streams {
    path := filepath.Join(binding.WorkDir, "logs", "latest.log")   // ← 三段硬编码
    sourceID := binding.TargetID + "/file"
```

- 无任何按实例核心类型的分支判断
- `RegisterInstance(uuid, targetID, generation, mode, workDir)` 入参**无核心类型**，即使想分支也缺信息
- **BungeeCord 的 `proxy.log.0`（根目录、非 `logs/` 子目录）因此永远不会被发现**

**实测对比**：

| 项目 | Paper (login-01) | BungeeCord (bungee-main) |
|---|---|---|
| 活动日志 | `logs/latest.log` | `proxy.log.0`（**根目录**） |
| 轮转历史 | `logs/*.gz` × 80 | `proxy.log.1/2...`（无 gz） |
| VL 采集量 | ✅ 2465 条/24h | ❌ **0 条**（未登记为源） |

**目标**：以**规则匹配**取代硬编码路径，内置四种核心类型的规则，支持实例级覆盖。

**非目标**（范围外）：
- 不做自定义正则/通配符规则 DSL（四条内置规则 + 路径覆盖已满足当前需求）
- 不改动采集账本、WAL、投递语义（本项只改**路径发现**这一环）

---

## 2. 需求（要什么）

1. **核心类型可见**：Worker 在登记实例日志时能获知该实例的核心类型（Paper / BungeeCord / Waterfall / Velocity / 通用）。
2. **规则匹配**：以规则表决定 `活动日志路径` 与 `归档 glob`，取代 `WorkDir/logs/latest.log` 硬编码。
3. **四条内置规则**：

   | 规则 key | 匹配核心 | 活动日志 | 归档 glob | 轮转语义 |
   |---|---|---|---|---|
   | `bukkit` | Paper / Spigot / Bukkit | `logs/latest.log` | `logs/*.gz` | 现有（FR-474 已实现） |
   | `bungee` | BungeeCord | `proxy.log.0` | `proxy.log.[0-9]*` | **新增**（根目录数字轮转） |
   | `waterfall` | Waterfall | `logs/latest.log` | `logs/*.gz` | 同 bukkit |
   | `velocity` | Velocity | `logs/velocity.log` | `logs/*.gz` | 同 bukkit |

4. **实例级覆盖**：把既有 `log_sources` 配置项接线到实例——当前它只服务 Worker/Node 日志，**未在实例注册路径被遍历**。
5. **向后兼容**：旧 CP / 旧实例（无核心类型信息）**回退到 bukkit 规则**，行为与现状逐字一致。

---

## 3. 设计（怎么做）

### 3.1 核心类型来源（不引入新数据模型）

生产实例表**已有可用依据**（2026-10-15 实测）：

- `role` 字段：已正确区分 `proxy`(1) / `backend`(11)
- `launch_spec.coreJar`：`BungeeCord.jar` vs `paper-1.20.1-196.jar`

**推导规则**（CP 侧）：

```
role == proxy && coreJar 含 "BungeeCord"  → bungee
role == proxy && coreJar 含 "Waterfall"   → waterfall
role == proxy && coreJar 含 "velocity"    → velocity
其余（role == backend）                   → bukkit
```

推导结果经 proto 增量字段下发（见 §3.2），**Worker 侧不再自行猜测**。

### 3.2 proto 扩展

`CreateInstanceRequest` 增量字段（延续既有 22/23/24 增量风格）：

| 字段号 | 名称 | 说明 |
|---|---|---|
| 25 | `log_rule_key` | 规则 key（`bukkit`/`bungee`/`waterfall`/`velocity`），空 = 回退 bukkit |
| 26 | `log_relative_path` | 实例级覆盖的活动日志相对路径（空 = 用规则的默认值）。**预留字段：CP 侧当前没有生产者**（`buildCreateInstanceRequest` 只填 `log_rule_key`），FR-503 的实例路径覆盖实际由心跳 `log.acq.path_overrides`（`HeartbeatResponse` 字段 23）承担；本字段保留供后续接线，Worker 侧读取逻辑一并在位 |

> **字段号选定依据（2026-10-15 实测核对）**：`CreateInstanceRequest` 当前已占用
> **1–20、22、23、24**（22/23/24 为日志采集增量字段 `log_target_id`/`log_acquire_mode`/
> `log_source_generation`）。**21 号在本 message 内空缺**，但同文件其他 message
> （`worker.proto:512`、`1137`）在用 21，且 21 曾出现在提交 `dcb00d56` 的历史中——
> 为规避历史删除字段的 wire 兼容风险，**不复用 21，改用 25/26**。
> 实现前须再核对一次 `proto/worker.proto` 当前实际占用。

**兼容性**：旧 CP 省略字段 ⇒ Worker 按 `log_rule_key` 为空处理 ⇒ 回退 bukkit ⇒ **行为与现状逐字一致**。

### 3.3 Worker 侧规则表

新增规则表（位置：`internal/worker/logs/ingest/rules.go`，与 `instances.go` 同包）：

```go
type Rule struct {
    Key            string   // bukkit / bungee / waterfall / velocity
    LiveRelative   string   // 相对 WorkDir 的活动日志路径
    ArchiveGlob    string   // 相对 WorkDir 的归档 glob（可为空）
}
```

`instances.go` 的路径解析改为：

```go
rule := rules.Lookup(binding.RuleKey)          // 空 key → bukkit
live := filepath.Join(binding.WorkDir, rule.LiveRelative)
if binding.RelativePathOverride != "" {        // 实例级覆盖优先
    live = filepath.Join(binding.WorkDir, binding.RelativePathOverride)
}
```

**归档 glob 派生**：`fileArchiveGlob`（`instances.go:224`）当前返回 `Dir(livePath)/*.gz`；bungee 规则的轮转文件不是 `.gz` 而是 `proxy.log.N`，故需按规则返回 glob（`proxy.log.[0-9]*`）。

### 3.4 `log_sources` 接线

`log_sources`（`config.go:668`）已有字段 `log_source_id / path / mode / rotate_to / archive_glob / storage_namespace`，但**只在 config.go 声明与 SetDefault，未在实例注册路径被遍历**。

接线方式：在实例注册时按 `storage_namespace == targetID` 匹配 `log_sources` 条目，命中则以其 `path` 覆盖规则默认值（等价于 §3.2 的 `log_relative_path`，但由 `worker.yml` 配置）。

**优先级**（高 → 低，已按**实际生效顺序**修正）：
1. CP 心跳下发的实例级覆盖 `log.acq.path_overrides`（即 `policy.PathOverrides`；在登记时**无条件压过**下两层）
2. `log_sources` 中匹配该实例的条目（运维在节点侧 `worker.yml` 的显式配置）
3. proto `log_relative_path`（**预留字段**：CP 当前没有生产者，见 §3.2）
4. 规则默认值（`log_rule_key` 决定）

> 手工配置冲突时的口径：两处都配了同一实例的路径 ⇒ 以 CP 侧（第 1 层）为准，节点侧
> `worker.yml` 的显式 path 会被静默压过。此顺序与 `internal/worker/logs/ingest/instances.go`
> 的登记路径一致，`internal/worker/grpc/log_acquisition.go` 只负责把第 2 层提到第 3 层之前。

**目标形态校验（2026-10-19 补，启动即拒）**：既然第 2 层靠 `storage_namespace == targetID`
匹配，命名空间就必须是**可被 CP 枚举到的目标形态**——`inst:<实例ID>` 或 `node:<节点ID>`
（由 `validateLogSources` 在 `config.Load` 阶段校验，见 `internal/worker/config.go`）。
`storage_namespace` 为空、写成自造前缀（如 `dev:smoke`）、`log_source_id` 不以自身命名空间
开头、`source_category` 越界、同一源重复登记，均拒绝启动并给出可执行的错误说明。

> 为什么必须拒：写错命名空间时**采集与写 VL 全部正常**，只是 CP 按目标枚举永远匹配不到
> → `target_missing_in_worker_response` → 归一化为 `ENGINE_NOT_READY` → 日志页显示
> 「日志查询服务未启用」。现场会一路去怀疑 VL，而真因只是命名空间不对（2026-10-17 dev
> 实测：`dev:smoke` 即触发该现象；改为 `node:1` 后同一查询立即从 0 行恢复为 5 行、
> `target` 状态 `partial` → `success`）。

### 3.5 不改动的部分（重要）

- 采集账本 / WAL / 四水位 / 投递语义：**零改动**
- `source_generation` 构造：**零改动**（仍为 `instance:<uuid>@worker:<uuid>`）
- `storage_namespace`：仍为 `targetID`（`inst:317`）——规则只影响**物理路径发现**，不影响源标识

---

## 4. 任务拆分

- [ ] 新增 `rules.go` 规则表与 `Lookup(ruleKey)`，含四条内置规则
- [ ] 扩展 proto `CreateInstanceRequest` 增量字段 25/26，CP 侧按 §3.1 推导并下发
- [ ] 改 `instances.go` 路径解析（替换硬编码），`fileArchiveGlob` 按规则返回
- [ ] `log_sources` 接线到实例注册路径，实现 §3.4 优先级
- [ ] 向后兼容：空 `log_rule_key` 回退 bukkit，行为与现状逐字一致
- [ ] 单元测试：四条规则各自的路径解析；实例级覆盖优先；旧实例回退
- [ ] 真机验收：BungeeCord `proxy.log.0` 在 VL 可查

---

## 5. 验收标准

- [x] **BungeeCord 实例的 `proxy.log.0` 在 VL 中可查到**（真机过：本机 `bungee-main`，此前 0 条；
      端到端验证——追加标记行后位置推进 80 字节且该行入库）
- [x] **Paper 实例采集行为逐字不变**（回归绿：`logs` 全量 19 包通过；
      `TestRegisterInstanceBukkitRuleKeepsHistoricalPaths` 断言路径逐字一致）
- [x] 实例级覆盖路径生效（`TestRegisterInstanceRelativePathOverridesRule` +
      `TestRegisterInstanceAbsolutePathOverride` 覆盖相对/绝对两种形态）
- [x] 新实例注册即自动匹配正确规则（CP 侧 `deriveLogRuleKey` 16 例验证；
      `TestRegisterInstanceBungeeRuleUsesRootProxyLog`）
- [x] 旧 CP 兼容：省略新字段时回退 bukkit
      （`TestRegisterInstanceBukkitRuleKeepsHistoricalPaths` 的 empty 子例 +
      `TestLookupFallsBackToBukkit`）
- [x] `go test ./internal/worker/logs/...` 全绿（19 包）
- [x] 真实环境验收证据与自动化测试分开记录（见本文档头部「真机验收」段）

**实现中新增的验收项**（原 spec 未预见，均由真机问题驱动）：

- [x] 规则随 binding 持久化、重启后保持（`TestInstanceRuleBindingSurvivesRestart`，含变异验证）
- [x] 存量库补列迁移且不丢行（`TestOpenMigratesLegacyInstanceBindingColumns`，含变异验证）
- [x] 规则升级可平滑应用（`TestRegisterInstanceRuleOnlyChangeReappliesBinding`）
- [x] 路径类变更仍被拒绝（`TestRegisterInstancePathChangeRejectedWithoutTransition`）
- [x] 规则升级不重读历史内容（`TestRuleChangeStartsAtEOFAvoidingReimport`，含变异验证）

---

## 6. 风险 / 待定

- **BungeeCord 轮转语义待实测**：`proxy.log.0` 轮转时旧文件是重命名为 `proxy.log.1` 还是被覆盖？需在真机观察一个轮转周期后确认 glob 形态（`proxy.log.[0-9]*`）是否正确。
- **Velocity 规则未实测**：本集群无 Velocity 实例，规则按官方默认路径 `logs/velocity.log` 预置，**待真实实例验证**。
- **Waterfall 规则未实测**：同上，按「与 Bungee 同源」预置为 `logs/latest.log`，待验证。
- proto 字段号 **25/26** 已在 §3.2 核对（21 因同文件他处占用与历史曾用而规避）；实现前须再核对一次。
