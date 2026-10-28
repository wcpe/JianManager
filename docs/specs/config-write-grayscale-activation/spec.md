# FR-530 / FR-531 / FR-532 配置落盘、灰度与生效

> 状态：草拟 · 优先级：P0 · 依赖：FR-529（分层覆盖）
> 关联：ServerProbe 配置重载能力（已交付）

## 1. 背景

- **FR-530**：`Worker.WriteConfig` 只做 `os.WriteFile` + 格式校验，**不读回、不校验、不确认**写入是否真的成功
- **FR-531**：`ConfigBaseline.Converge` 是**循环逐台**，无批次、无观察窗、无熔断
- **FR-532**：JM 写完配置**不触发任何生效动作**，插件继续用旧配置

三件事串起来才构成完整的"改配置 → 落盘 → 生效"闭环。

---

# FR-530 落盘与读回校验

## 2. 写入流程

```
CP（持有期望内容与 hash）
  │ gRPC WriteConfigWithVerify(path, content, expectedHash)
  ↓
Worker
  1. 路径解析与越界检查
  2. 写前快照（备份原文件）
  3. 原子写入（临时文件 + rename）
  4. 读回文件并计算 sha256
  5. 与 expectedHash 比对
  6. 返回结果
  ↓
CP
  记录版本、触发后续生效
```

## 3. 关键实现要求

### 3.1 原子写入

```
1. 写入同目录临时文件 <path>.tmp.<uuid>
2. fsync
3. os.Rename(tmp, path)   ← 原子替换
4. fsync 目录（保证 rename 持久化）
```

**为什么必须原子**：直接 `os.WriteFile` 在写入过程中崩溃会留下半截文件，插件启动时读到的就是坏配置。

现有代码直接 `os.WriteFile(path, ...)`，**这是要修的缺陷**。

### 3.2 路径越界检查

```go
func resolveSafePath(workDir, relPath string) (string, error) {
    clean := filepath.Clean(relPath)
    if filepath.IsAbs(clean) { return "", ErrAbsolutePath }
    if strings.HasPrefix(clean, "..") { return "", ErrPathEscape }
    abs := filepath.Join(workDir, clean)
    if !strings.HasPrefix(abs, workDir+string(os.PathSeparator)) {
        return "", ErrPathEscape
    }
    return abs, nil
}
```

必须处理：绝对路径、`..` 穿越、符号链接逃逸（用 `filepath.EvalSymlinks` 二次校验）。

### 3.3 读回校验

写入后必须：

1. 重新打开文件读全量
2. 计算 sha256
3. 与 CP 传来的 `expectedHash` 逐字节比对
4. 不一致 → 返回 `WRITE_VERIFY_FAILED`（这说明磁盘有问题或有并发写）

### 3.4 写前快照

| 项 | 要求 |
|---|---|
| 位置 | `<workDir>/.jm-backup/<path>.<timestamp>` |
| 触发 | 文件已存在且内容不同 |
| 保留 | 最近 N 个（默认 10），超出删最旧 |
| 用途 | 回滚（FR-531）、审计追溯 |

### 3.5 并发保护

同一实例同一路径的并发写入必须串行化（实例级 mutex + 路径 key），否则会出现：
- 两个写同时 rename，后者覆盖前者但 CP 以为都成功
- 快照被覆盖

## 4. 接口

```protobuf
rpc WriteConfigWithVerify(WriteConfigRequest) returns (WriteConfigResponse);

message WriteConfigResponse {
  bool   success       = 1;
  string error         = 2;
  string error_code    = 3;   // PATH_ESCAPE / WRITE_FAILED / WRITE_VERIFY_FAILED
  string actual_hash   = 4;   // 读回后的实际 hash
  bool   backup_created = 5;
  string backup_path   = 6;
}
```

## 5. 验收

1. 写入正确内容 → hash 一致，返回 success
2. 人为篡改磁盘内容（模拟并发写）→ 返回 `WRITE_VERIFY_FAILED` 且 `actual_hash` 与期望不同
3. 绝对路径 / `..` / 符号链接逃逸 → `PATH_ESCAPE`
4. 写入过程中 kill -9 → 原文件完好（原子性）
5. 并发写同一路径 → 串行化，最终内容与最后一次请求一致
6. 快照数量超过保留数 → 最旧的被清理

---

# FR-531 分批灰度与编排

## 6. 批次模型

```go
type DeliveryOrder struct {
    ID           uint
    ConfigFileID uint              // 关联的配置文件
    TargetLayer  ScopeRef          // 应用哪一层的版本
    Selector     TargetSelector    // 目标实例集（复用 FR-535 的解析）
    BatchMode    string            // percent | count
    BatchSizes   []int             // [10, 30, 60]
    ObserveWindowSec int           // 默认 120
    FailureRateThreshold      int  // 失败率 %，0=关
    UnhealthyRateThreshold    int  // 不健康率 %，0=关
    ActivationMethod string        // hot_reload | restart | push_only
    ActivateTimeoutSec int       // 默认 300
    Status       string
}
```

## 7. 状态机

```
draft → pending_approval → approved → rolling → observing → completed
                                       ↓         ↓
                                     paused    failed
                                       ↓
                                  rolling_back → rolled_back
```

### 7.1 目标级状态

| 状态 | 含义 |
|---|---|
| `pending` | 未下发 |
| `pushing` | 下发中 |
| `pushed` | 落盘成功 |
| `activating` | 生效中 |
| `activated` | 生效成功 |
| `failed` | 失败（含原因码） |
| **`deferred`** | **离线/暂不可达，不计入失败率** |
| `skipped` | 主动跳过（熔断或终止） |

## 8. 离线语义（关键设计）

### 8.1 与 Beacon 的差异

Beacon 原设计：agent 离线 → `failed` → 计入熔断率（理由："离线面大说明集群不健康，不该继续放量"）。

**问题**：几台机器正常重启就会把整个变更打停，日常运维无法使用。

### 8.2 JM 的设计

| 离线率 | 行为 |
|---|---|
| **< 10%** | 标 `deferred`，**不计入失败率**，继续推在线目标；目标上线后自动补推（可开关） |
| **≥ 10%** | 熔断（保留 Beacon 的安全语义） |

**离线率计算**：`offline / planned_count`，在每批开始时计算。

**自动补推**：

- 开关 `autoRetryDeferred`（默认 true）
- 时机：探针重新连上时（`connected` 事件）
- 限制：同一目标最多重试 3 次，超出标 `failed`

### 8.3 为什么这样更合理

- 正常重启（1-2 台）不会打停变更
- 半个集群失联（≥10%）仍然会停 —— 这是真正的异常信号
- `deferred` 与 `failed` 语义分离，运维一眼能看出"是目标不在"还是"是配置坏了"

## 9. 熔断

### 9.1 计算口径

```
失败率 = failed / (planned_count - deferred) × 100
```

**注意分母排除 `deferred`** —— 否则离线目标会虚高失败率。

### 9.2 触发时机

目标每次进入终态（`activated` / `failed`）时**即时评估**，不等整批结束。

提前熔断可以中止尚未下发的目标。

### 9.3 熔断动作

| 动作 | 说明 |
|---|---|
| 当前批置 `failed` | 记录触发阈值与实测值 |
| 单置 `paused` | `pauseKind=circuit_break` |
| 批内未下发目标 | 置 `skipped` |
| 后续批 | 保持 `pending`（不自动继续） |
| 审计 | `actor=system`，记录 `break_reason` |

## 10. 回滚

### 10.1 整批回滚

1. 每个目标从 `.jm-backup` 找最近快照
2. 逐目标还原
3. 配置版本回退到变更前（`config_from_version_id`）
4. 重新触发一次生效

### 10.2 目标级子集回滚

只还原指定目标，**配置版本不回退**。适用于"只有这几台出问题"。

### 10.3 回滚的幂等

回滚本身也可回滚（还原回滚前的状态），需记录 `rollback_of_rollback`。

## 11. 接口

```http
POST   /api/v1/config-deliveries                    创建（draft）
GET    /api/v1/config-deliveries                    列表
GET    /api/v1/config-deliveries/:id                详情
PUT    /api/v1/config-deliveries/:id                编辑（仅 draft）
DELETE /api/v1/config-deliveries/:id                删除（仅 draft）
POST   /api/v1/config-deliveries/:id/submit         提交审批
POST   /api/v1/config-deliveries/:id/pause          暂停
POST   /api/v1/config-deliveries/:id/resume         继续（retry_failed / skip_failed）
POST   /api/v1/config-deliveries/:id/cancel         终止
POST   /api/v1/config-deliveries/:id/rollback       回滚（可带 serverIds 子集）
GET    /api/v1/config-deliveries/:id/targets        目标分页
GET    /api/v1/config-deliveries/:id/impact         影响预览
GET    /api/v1/config-deliveries/:id/observe        观察窗数据
```

## 12. 验收

1. 12 实例，2 台离线（16%）→ 熔断
2. 100 实例，5 台离线（5%）→ 标 `deferred`，95 台继续，全部完成后可补推
3. 补推在目标上线时自动触发，最多 3 次
4. 失败率计算分母排除 `deferred`
5. 提前熔断可中止未下发目标
6. 整批回滚与子集回滚各自正确
7. 回滚本身可再回滚

---

# FR-532 生效编排

## 13. 生效方式

| 方式 | 行为 | 适用 |
|---|---|---|
| `hot_reload` | 下发 `core.config_reload` 到探针，由插件重载 | 插件支持热重载 |
| `restart` | Worker 停实例 → 起实例 → 等健康回归 | 必须重启才生效 |
| `push_only` | 只落盘，不触发任何动作 | 下次启动自然生效 |

## 14. hot_reload 流程

```
CP → Worker → ServerProbe core.config_reload
                ↓
        插件 reloadConfig()
                ↓
        回执 { reloaded: [...], failed: [{path, code, message}], costMs }
```

### 14.1 回执语义（必须诚实）

**这是最容易误导人的地方**：

| 插件返回 | JM 记录 | UI 文案 |
|---|---|---|
| `reloaded` | `activated` | "配置已重新加载" |
| `failed` + code | `failed` | "重载失败：<原因>" |
| 超时 | `failed` (TIMEOUT) | "重载超时，配置可能未生效" |
| `BEST_EFFORT`（插件未实现确切回执） | `activated` + 标记 | **"已请求重载，插件未提供确认"** |

**绝对禁止**把 `BEST_EFFORT` 显示成"已生效"。Bukkit 的 `reloadConfig()` 是 void 返回，插件吞了异常探针也无从得知。

### 14.2 超时

- `core.config_reload` 超时**独立配置**（默认 10s），不复用业务命令 5s
- 超时后不能强杀主线程（Bukkit 不允许），只能标记失败
- 超时的路径进「熔断名单」，短时间不自动重试，避免反复卡服

## 15. restart 流程

```
1. Worker 停止实例（优雅关服，超时强杀）
2. 等待进程退出
3. Worker 启动实例
4. 等待健康回归：
   - 进程存活
   - 探针重新连上（若实例有探针）
   - 达到 activateTimeoutSec 仍未回归 → failed
```

**"关了没起来"必须判失败**并计入熔断 —— 这是最危险的失败模式。

## 16. 生效结果与配置状态的关系

| 落盘 | 生效 | 最终状态 | 含义 |
|---|---|---|---|
| ✅ | ✅ | `activated` | 完整成功 |
| ✅ | ❌ | `failed` | 配置已写入但未生效（**需人工介入**） |
| ✅ | 超时 | `failed(TIMEOUT)` | 不确定是否生效 |
| ❌ | — | `failed` | 未写入 |

**关键**：`落盘成功但生效失败`必须明确区分，因为此时磁盘上是新配置、内存里是旧配置，重启就会变。

## 17. 验收

1. `hot_reload`：插件成功重载 → `activated`
2. `hot_reload`：插件返回失败 → `failed` 且带插件提供的错误码
3. `hot_reload`：插件未实现确切回执 → `activated` + `BEST_EFFORT` 标记，UI 文案不夸大
4. `hot_reload` 超时 → `failed(TIMEOUT)`，路径进熔断名单
5. `restart`：正常启停 → `activated`
6. `restart`：启动失败 → `failed` 且计入熔断
7. `push_only`：只落盘，实例状态不变
8. 落盘成功但生效失败时，审计明确记录两个状态

---

## 18. 待定

1. 观察窗期间采集哪些健康指标（TPS？探针连接？玩家数？）
2. `restart` 的优雅关服超时（默认 30s？）
3. 熔断名单的重试窗口（5 分钟？）
4. 是否需要「分阶段生效」（先 reload 部分文件，再 reload 其余）

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-530 配置写入经 Worker 落盘 + 读回校验（feat，需 spec，spec

**需求补充**：`docs/specs/config-write-grayscale-activation/spec.md`）：CP 不直接落盘，一律经 gRPC 下发到 Worker；Worker 写完后**读回并计算 sha256**，与 CP 期望值比对，不一致返回明确错误；写前自动快照原文件（供回滚）；路径必须是相对路径，以实例工作目录为根，越界拒绝（`PATH_ESCAPE`）。**现状缺口**：`Worker.WriteConfig` 只做 `os.WriteFile` + 格式校验，不读回、不校验、不确认。验收：写入错误 hash 能被检出；越界路径被拒；写前快照存在且可回滚。

**交付状态**：✅ 已交付@v0.24.0

### FR-531 配置分批灰度与编排（feat，需 spec，spec

**需求补充**：`docs/specs/config-write-grayscale-activation/spec.md`，依赖 FR-529/530）：把 `ConfigBaseline.Converge` 的「循环逐台」升级为**分批灰度**：批次策略 `percent`/`count`（如 `[10,30,60]`）、观察窗（默认 120s）、熔断（失败率阈值 + 不健康率阈值）、整批回滚 + 单目标子集回滚。**离线语义**：离线率 < 10% 标 `deferred` 不计入失败率并继续推在线目标、上线后自动补推（可开关）；离线率 ≥ 10% 熔断。按 100+ 实例规模设计。验收：12 实例中 2 台离线→变更继续且可补推；6 台离线→熔断；回滚可整批也可单目标。

**交付状态**：✅ 已交付@v0.24.0

### FR-532 配置生效编排（feat，需 spec，spec

**需求补充**：`docs/specs/config-write-grayscale-activation/spec.md`，依赖 FR-530/531）：配置落盘后按 `activationMethod` 触发生效——`hot_reload`（下发 ServerProbe `core.config_reload`）、`restart`（停→启→等健康回归）、`push_only`（只落盘）；`activateTimeoutSec` 默认 300，超时判失败；**回执如实记录插件声明结果**（含 `TIMEOUT` / `BEST_EFFORT`），**不得把「reload 成功」写成「已生效」**（措辞须准确）。`core.config_reload` 超时独立配置，不复用业务命令 5s。**现状缺口**：JM 写完配置不触发任何生效动作。验收：三种模式各有测试；超时被正确判定；审计措辞不夸大。

**交付状态**：✅ 已交付@v0.24.0

### FR-530

配置落盘校验

✅ 代码完成（2026-10-24）：`worker/grpc/config_write.go`（原子写入：临时文件+fsync+rename+目录 fsync；写前快照保留 10 份；读回 sha256 与期望值比对返回 `WRITE_VERIFY_FAILED`；路径越界 `PATH_ESCAPE` 含符号链接二次校验；实例级串行化）+ proto 扩展 `expected_hash`/`actual_hash`/`backup_created`。修复了原先 `os.WriteFile` 崩溃留半截文件的缺陷。


### FR-531

配置分批灰度

✅ 代码完成（2026-10-24）：`model/config_delivery.go`（手写转移表）+ `service/config_delivery.go`。分批 percent/count、观察窗、熔断（失败率分母**排除 deferred**）、整批与子集回滚、回滚可再回滚。**离线语义与 Beacon 的差异已落地**：离线率 <10% 标 `deferred` 不计失败率且上线自动补推（最多 3 次），≥10% 熔断。过程中修掉 GORM 零值陷阱（`default:true` 让「显式关掉补推」静默落成 true）。


### FR-532

配置生效编排

✅ 代码完成（2026-10-24）：`service/config_activation.go`。三种 `activationMethod`；`BEST_EFFORT` 用三个正交布尔（Confirmed/BestEffort/TimedOut）而非单枚举，落库为 `activated + activateConfirmed=false + bestEffort=true`，文案「已请求重载，插件未提供确认」——**绝不写成「已生效」**。`restart` 必须用 `InstanceService.Restart` 而非 Stop+Start（异步委托会撞启动预检）。

