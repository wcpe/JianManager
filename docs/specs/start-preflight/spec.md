# 功能规格：实例启动同步预检

> 状态：✅ done（v0.16.0 交付，真机验收）　·　关联 PRD：FR-314（增强 FR-005 启停链路）　·　分支：feature/fr-314-start-preflight

## 1. 背景与目标

真机复现（2026-07-13，实例 2）：实例配置有误（未绑 JDK 且 PATH 无 java）时，点「启动」CP 立即受理并转 STARTING，失败要等异步委托回写才变 CRASHED——用户看到「启动中→崩溃」兜一圈，且原因藏在 statusReason 里无处可见。

Worker 侧其实已有启动内嵌预检（`internal/worker/process/preflight.go` 的 `preflightJavaVersion`，BUG-012 引入），但它跑在 `StartInstance` RPC 内部、失败只能走异步 CRASHED 回写路径。本 FR 把预检**升格为独立同步 RPC**：点启动先同步预检，失败立刻把具体原因还给点击动作、状态不进 STARTING；过检才走既有异步启动。

与 FR-316（搭建向导时的版本-JDK 兼容拦截）互补：FR-316 拦在搭建时，本 FR 拦在启动时；与 FR-312（原因横幅）协同：预检失败写入 statusReason，横幅自然可见——但二者无硬依赖，独立交付各自成立。

## 2. 需求（要什么）

- 点「启动」时，CP 先同步调 Worker 预检，全部通过才 `transition(STARTING)` + 异步委托启动。
- 预检失败：HTTP 同步返回具体失败项与原因，实例状态**保持 STOPPED 不闪 STARTING**，同时把原因写入 `statusReason`（供 FR-312 横幅/卡片展示）。
- 预检项（daemon/direct 进程型）：
  1. **java_runtime**：复用既有 `preflightJavaVersion` 规则（绑定 JDK→探测绑定 bin/java 可运行；未绑定→PATH java 可运行且大版本 ≥17；非 java 命令跳过）。
  2. **work_dir**：实例工作目录存在且为目录。
  3. **launch_target**：启动命令可解析出 `-jar <path>` 时，jar（相对工作目录解析）必须存在；解析不出 jar（自定义命令）则**保守放行**，不误拦。
- 节点未连接（连接池无客户端）：同步返回「节点未连接」，不再走异步 CRASHED 回写。
- 老 Worker 兼容：预检 RPC 返回 `Unimplemented` → 跳过预检，行为与现状完全一致。
- 范围内：`POST /instances/:id/start` 路径；proto 新 RPC；前端启动失败 toast 展示预检信息。
- 不做（范围外）：restart/stop 路径预检；docker 进程型深度预检（镜像存在性等，整体跳过预检直接放行）；FR-316 搭建向导拦截；FR-313 崩溃快照；`StartInstance` 内既有内嵌预检**保留不动**（纵深防御，防绕过 CP 直连场景与竞态窗口）。

## 3. 设计（怎么做）

### proto（`proto/worker.proto`）

```proto
// WorkerService 新增：
rpc PreflightStartInstance(InstanceActionRequest) returns (PreflightResult);

message PreflightResult {
  bool ok = 1;                       // 全部通过
  repeated PreflightCheck checks = 2; // 逐项结果（含通过项，便于前端完整呈现）
}
message PreflightCheck {
  string name = 1;     // java_runtime | work_dir | launch_target
  bool ok = 2;
  string message = 3;  // 失败时为面向用户的中文原因（沿用 preflight.go 既有文案风格）
}
```

单消息极小，隧道/直拨两路皆走 unary，不涉 FR-305 尺寸守卫边界。

### Worker（`internal/worker/`）

- `process.Manager` 新增 `PreflightStart(instanceID) []CheckResult`：按注册的实例规格聚合三项检查；docker 进程型直接返回 ok（附说明）；实例未注册返回明确错误。
  - java_runtime：调既有 `preflightJavaVersion`（不改其规则与文案）。
  - work_dir：`os.Stat` 实例工作目录。
  - launch_target：从最终 StartCommand（结构化派生已在 CP 完成）解析 `-jar` 后继 token，相对工作目录 `os.Stat`；无 `-jar` 可解析 → ok。
- `internal/worker/grpc/server.go` 新增 `PreflightStartInstance` handler：查实例→调 `PreflightStart`→组 `PreflightResult`。

### Control Plane（`internal/controlplane/service/instance.go`）

`Start`（HTTP handler 调的 service 方法）在 `transition(STARTING)` **之前**插入：

1. 查节点 + `pool.Get`：未连接 → 返回业务错误 `NODE_OFFLINE`（HTTP 409），不改状态。
2. `registerOnWorker(instance)`（与 delegate 同款 ensure，防 Worker 重启后实例未注册）。
3. 调 `PreflightStartInstance`（超时 10s）：
   - `Unimplemented` → 跳过（老 Worker），继续现流程。
   - 其他 RPC 错误 → 按预检失败处理（报「预检不可达: …」）。
   - `ok=false` → 拼接失败项 message，写 `statusReason`（status 保持 STOPPED），返回 `PREFLIGHT_FAILED`（HTTP 422，body 携带 `checks` 明细）。
4. 通过 → `transition(STARTING)`（清 reason）+ 异步 `delegateToWorker("start")`，与现状一致。

### 前端（`web/src/`）

- 启动 mutation 失败：toast 展示 API 返回 message（422 预检失败原因全文）；`checks` 明细可并入 toast 描述（逐项一行）。
- mock handlers（`web/src/mocks/`）：start endpoint 增加可注入的 422 PREFLIGHT_FAILED 形态。
- 无新页面/组件；原因横幅是 FR-312 职责（statusReason 已由本 FR 写入，天然协同）。

### API（`docs/API.md` 同步）

`POST /api/v1/instances/:id/start` 新增响应：
- `409 NODE_OFFLINE`：节点未连接。
- `422 PREFLIGHT_FAILED`：`{ error, message, checks: [{name, ok, message}] }`。
权限不变（沿用既有实例操作权限）。

## 4. 任务拆分

- [ ] proto：`PreflightStartInstance` RPC + `PreflightResult`/`PreflightCheck` message，重新生成 pb
- [ ] Worker：`Manager.PreflightStart` 三项检查聚合（复用 `preflightJavaVersion`，新增 work_dir / launch_target）+ 单测（表驱动：java 缺/低版本、workdir 缺、jar 缺、非 java 跳过、docker 放行、未注册报错）
- [ ] Worker gRPC handler + 单测
- [ ] CP：`Start` 接同步预检（节点未连 409 / Unimplemented 回退 / 422 + statusReason 落库 + 状态不动）+ 单测（含回退路径与「失败后 status 仍 STOPPED、reason 已写」断言）
- [ ] 前端：启动失败 422 toast 展示 message+checks；mock 注入形态 + vitest
- [ ] 文档同步：PRD 状态（🔨 开发中）、API.md（409/422 形状）、ARCHITECTURE.md（WorkerService RPC 表加一行）、CHANGELOG 末尾追加

## 5. 验收标准

1. 未绑 JDK 且节点无 java：点启动**同步**得到 422 与「实例未绑定 JDK 且 PATH 上无可用 java…」全文，实例状态全程 STOPPED（无 STARTING 闪烁），`statusReason` 已写入。
2. jar 缺失（绑好 JDK）：点启动同步得到 422 与 launch_target 失败明细。
3. 配置齐全：点启动行为与现状一致（202 语义、STARTING→RUNNING），`StartInstance` 内嵌预检不重复报错。
4. 节点未连接：同步 409「节点未连接」，不再产生异步 CRASHED 回写。
5. 老 Worker（无该 RPC）：启动行为与现状完全一致（回退路径有测试锁定）。
6. go test / vitest 全绿；新增核心逻辑覆盖 ≥80%（preflight 聚合与 CP 分支）。
7. **真机（需用户确认）**：103.45.143.199 面板用一次性实例复现场景 1/2（不动实例 2 的绑定与 jar）；实例 2 正常启动回归通过；**验收完实例必停**（OOM 纪律）。

## 6. 风险 / 待定

- launch_target 解析保守放行：自定义非 `-jar` 命令不拦（宁漏勿误伤）；后续 FR-313 崩溃快照兜住漏网场景。
- 预检与启动之间存在 TOCTOU 窗口（预检过后 jar 被删）：接受——`StartInstance` 内嵌预检与进程崩溃路径仍在，本 FR 不追求原子。
- CP→Worker 预检超时 10s 取值：慢隧道下点击等待上限；若真机体感差可降为 5s（实现时以常量集中定义）。

## 7. 实现说明（与设计偏差，落地后补记）

- **复用 `InstanceActionResponse` 而非新增 `PreflightResult`/`PreflightCheck`**：本机 protoc（28.3）与项目基线（27.1）描述符编码不一致，整文件重生成会引入大量无关漂移；且拼接后的失败原因单串已满足展示需求，逐项 checks 数组非必需。故新 RPC `PreflightStartInstance` 复用 `InstanceActionResponse`（`success`=全部通过，`error`=拼接失败原因），grpc 桩按既有 `StartInstance` 模式手工增补（含 `Unimplemented` 兜底，老 Worker 向后兼容白送）。
- **前端无需改动**：既有启动 mutation 的 `onError` 已 `toast.error(err.response?.data?.message)`，422 的 message 即拼接后的预检原因，天然弹出；再由 FR-312 失败原因横幅在控制台回显 statusReason。故本 FR 不新增前端代码。
- **CP `Start` 挂载点**：预检插在既有 `memoryGate` 之后、`transition(STARTING)` 之前；`ErrNodeOffline` 复用 `jdk.go` 既有哨兵。
