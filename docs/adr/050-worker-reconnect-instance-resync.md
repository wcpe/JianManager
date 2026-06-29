# ADR-050: Worker 重连后由 CP 重推实例规格同步注册表

- **日期**: 2026-06-29
- **状态**: accepted
- **上下文**: Worker Node 不持久化自己的实例注册表——`process.Manager` 的实例表是纯内存态。Worker 重启后只有 `RecoverDaemonInstances`（`internal/worker/process/manager.go`）经 PID 文件把**仍存活的 daemon wrapper**重连为 `RUNNING`（ADR-003）；**所有 STOPPED 实例在 Worker 侧完全消失**（没有进程、没有 PID 文件可扫）。而 Worker 的全部文件/配置/归档/备份/克隆类 RPC（`internal/worker/grpc/*_ops.go`）都以 `s.manager.GetInstance(req.InstanceUuid)` 定位实例的工作目录，实例不在内存表即返回「实例 X 不存在」。后果（bug #2）：Worker 重启后，对任一既有 STOPPED 实例做文件浏览/读写、配置编辑、归档浏览、备份/克隆，CP 侧全部报「实例不存在」，管理面与浏览面双双失效，直到该实例被重新「启动」（`delegateToWorker` 的 start 分支会先 `registerOnWorker` 补注册）才恢复——但用户期望停机实例也能管文件，不必先拉起。
  根因是**注册表只在内存、且只有 RUNNING 路径会在重启后被重建**。CP 侧虽已有「心跳对账」把 Worker 未上报的 RUNNING 实例在 DB 置回 STOPPED（`syncInstanceStates`），但那只修 DB 状态、**不会让 Worker 重新认识实例**。

## 决策

**Worker 重连/重注册成功后，由 Control Plane 主动把该节点的全部实例规格重推给 Worker；Worker 用它填充内存注册表（状态置 STOPPED、不启动任何进程），使文件/配置/归档等定位类 RPC 能解析工作目录。** CP 是实例的单一真源（DB），Worker 不持久化、不直连 DB（架构不变量），因此「重启后谁来补全 Worker 的认知」只能是 CP。

### 1. 为何 CP 重推，而非 Worker 本地持久化

- **守架构不变量**：`Worker Node 不得直接访问数据库，所有持久化通过 Control Plane API 或 gRPC`（architecture-invariants.md「数据所有权」）。让 Worker 自己把实例表落盘，会引入第二份实例真源，和 DB 产生漂移（实例改名/改命令/换 JDK/删除后 Worker 落盘副本过期），还要解决两份真源的对账——得不偿失。
- **CP 已是真源且已有重推能力**：`InstanceService.registerOnWorker`（`internal/controlplane/service/instance.go`）早已能把一条 `model.Instance` 完整翻译成 Worker 的实例规格（解析绑定 JDK 路径、解出 EnvVars JSON、派生优雅停止命令、docker 端口/限额）下发；`EnsureRegistered` 也已是幂等补注册入口（其注释明言「STOPPED 实例在 Worker 重启后可能不在管理器中」）。重连同步只是把「单实例补注册」扩展为「整节点重推」，复用既有翻译逻辑，零新真源。
- **懒加载（op 报不存在时即时回查 DB 补注册）被否**：会把「实例定位」与「CP 反查」耦进每个 `*_ops.go`，违反 Worker 不直连 DB（Worker 无法查 DB，只能反向 RPC 回 CP，形成 Worker→CP 的业务反向依赖），且分散、难测。集中在「重连一次性重推」最简单、最易验证。

### 2. 推送时机：复用既有 `onWorkerConnect` 回调

CP 在两处确认「到某 Worker 的反向 gRPC 通道已就绪」并触发 `onWorkerConnect(nodeUUID)`（`internal/controlplane/grpc/handler.go`）：

1. **`Register` 成功**（首注册 / 重注册）后 `connectWorker` 建好反向连接池条目即回调；
2. **心跳重建**：CP 重启致反向连接池为空时，借心跳按节点 host+grpcPort 重连成功后回调。

二者正是「Worker（或 CP）重启后通道恢复」的信号。把实例重推挂在 `onWorkerConnect` 上，即同时覆盖「Worker 重启重注册」与「CP 重启心跳重连」两条恢复路径，无需新增触发时机。回调内既有 `eventSvc.StartWorkerStream` / `playerEventSvc.StartWorkerStream`，实例重推与之并列。

### 3. gRPC 机制：新增单向 `ResyncInstances` RPC，复用 `CreateInstanceRequest` 作规格元素

新增一个 **CP→Worker 单向（unary）RPC `ResyncInstances`**，一次携带该节点全部实例规格：

```proto
message ResyncInstancesRequest { repeated CreateInstanceRequest instances = 1; }
message ResyncInstancesResponse { int32 registered = 1; int32 skipped = 2; }
```

- **复用 `CreateInstanceRequest` 作每实例规格**：该 message 已含 Worker 填注册表所需的全部字段（instance_uuid / name / process_type / start_command / stop_command / work_dir / env_vars / auto_restart / jdk_path / probe_port / graceful_stop_timeout / docker 镜像端口限额）。零新规格 message，CP 侧用与 `registerOnWorker` **完全相同**的翻译代码逐条构造（抽出共享 builder 去重）。
- **为何新 RPC 而非 N 次 `CreateInstance`**：① 语义明确——「重连同步」不是「创建」，避免 Worker 日志刷「实例已创建」噪声、避免把 Create 语义当幂等补注册滥用；② **一次往返补全整节点**，N 个实例一个 RPC，明显优于 N 次往返（应对大量实例的开销，见 §5）；③ Worker 侧可在一个 handler 内统一「已存在则跳过、不存在则按 STOPPED 注册」，把"不打扰 RUNNING 恢复实例"的不变量收敛到一处。
- **为何不扩 `RegisterResponse` 带实例列表**：`Register` 由 Worker 发起、CP 应答，但 CP 在应答 `Register` 的那一刻**反向连接池可能尚未建好**（`connectWorker` 在返回响应前 `pool.Connect`，但 Worker 侧 gRPC server 是否已 ready 取决于启动时序）；把实例列表塞进 `RegisterResponse` 还要求 Worker 注册逻辑（`register` 包）反向依赖 `process.Manager` 来消费——耦合错位。用独立的 CP→Worker RPC，调用方是 CP（已确认通道就绪），被调用方是 Worker gRPC server（持有 manager），依赖方向顺。
- **不引入双向 stream**：实例集合是**有界快照**，一次 unary 推完即可；状态的持续变化本就由既有心跳（Worker→CP 上报 InstanceState）+ 状态事件流覆盖，无需为重推单开 stream。

### 4. Worker 收到后如何填注册表

`ResyncInstances` handler（`internal/worker/grpc/server.go`）逐条：

- **已在内存表（按 UUID）→ 跳过**（计入 `skipped`）。这保证 `RecoverDaemonInstances` 刚恢复的 **RUNNING** daemon 实例**不被重推覆盖**（重推只补 Worker 不认识的实例），也使重复重推幂等。
- **不在表 → 调 `manager.Create(...)` 注册为 STOPPED**：复用 `CreateInstance` 的同一套字段映射（抽出 `registerInstanceFromProto` 共享 helper），把 CP 下发的相对 WorkDir 经 `root.Abs` 解析为本节点绝对路径并 `MkdirAll`（与 `CreateInstance` 一致），docker 实例补 `SetDockerConfig`。**不调用 `Start`**——重推只恢复"可被定位"，绝不擅自拉起进程（停机实例必须保持停机）。
- 返回 `registered` / `skipped` 计数（供 CP 记日志与排障，不含敏感信息）。

### 5. 大量实例时的开销

- **一次 RPC 传整节点快照**：N 个实例的规格在一个 `ResyncInstancesRequest` 内，单次往返。每条规格是小结构（字符串字段为主），千级实例的请求体仍在常规 gRPC 消息体量级（远小于默认 4MB 上限）；真要爆量再分页，当前规模无需。
- **仅在重连/重注册触发**：不是每心跳重推，频率极低（Worker/CP 重启级事件）。
- **Worker 侧 O(N) 填表**：跳过已存在者，仅对缺失实例建表项，无进程启动开销（不 Start）。
- 失败不阻断：重推失败仅告警（与 `registerOnWorker` 失败的现有容错一致），下次重连/心跳重连再补；个别实例启动路径仍有 `registerOnWorker` 兜底补注册。

## 理由

- **单一真源、最小新增**：CP 仍是唯一真源，Worker 仍不碰 DB；复用既有规格翻译与既有 `onWorkerConnect` 触发点，新增面仅一个 unary RPC + 一个 Worker handler + 一个 CP 重推方法。
- **不打扰 RUNNING 恢复**：重推「只补不覆盖」，与 `RecoverDaemonInstances` 的 PID 重连正交，二者互不冲突。
- **不擅自拉起**：重推只让停机实例「可被文件/配置 op 定位」，不改变其 STOPPED 语义，符合用户预期（停机也能管文件，但不会被同步动作意外启动）。

## 后果

- proto 新增 `ResyncInstances` RPC + `ResyncInstancesRequest`/`ResyncInstancesResponse`（复用 `CreateInstanceRequest`），需 `make proto` 重新生成 `proto/workerpb/`。
- `internal/worker/grpc/server.go`：新增 `ResyncInstances` handler；抽出 `registerInstanceFromProto` 供 `CreateInstance` 与 `ResyncInstances` 共用字段映射。
- `internal/controlplane/service/instance.go`：抽出 `buildCreateInstanceRequest` 供 `registerOnWorker` 与新 `ResyncNode` 共用；新增 `ResyncNode(nodeUUID)` 查该节点全部实例、构造规格、单次调 `ResyncInstances`。
- `cmd/control-plane/main.go`：`SetOnWorkerConnect` 回调内追加 `instanceSvc.ResyncNode(nodeUUID)`（异步，不阻塞回调/心跳）。
- Worker 重启后既有 STOPPED 实例的文件/配置/归档/备份/克隆 op 不再报「实例不存在」。

## 替代方案

- **Worker 本地持久化实例表**：引入第二份真源、与 DB 漂移、要对账；违反 Worker 不直连 DB 的初衷（虽不直连 DB，但落盘副本同样制造真源二元性）。放弃。
- **懒加载：op 报不存在时 Worker 回查补注册**：Worker 无法查 DB，只能反向 RPC 回 CP，形成 Worker→CP 业务反向依赖，且把 CP 反查耦进每个 `*_ops.go`，分散难测。放弃。
- **扩 `RegisterResponse` 带实例列表**：`register` 包须反向依赖 `process.Manager` 消费列表，依赖错位；且应答 Register 时通道未必就绪。放弃，改用独立 CP→Worker RPC。
- **N 次复用 `CreateInstance` 做重推**：语义滥用 + N 次往返 + 日志噪声 + 难以把"跳过 RUNNING"收敛到一处。放弃，改用一次性批量 `ResyncInstances`。
- **双向 stream 持续同步**：实例集合是有界快照，状态变化已由心跳/事件流覆盖，stream 过重。放弃。

## 关系

- **ADR-002（gRPC 节点通信）**：`ResyncInstances` 是 CP→Worker 的 gRPC RPC，与既有实例操作 RPC 同构，未引入新 RPC 协议。
- **ADR-003（守护进程 Wrapper）**：`RecoverDaemonInstances` 经 PID 文件重连 RUNNING daemon 实例的逻辑不变；本 ADR 的重推「只补不覆盖」，与之正交——RUNNING 恢复实例在重推中按 UUID 命中被跳过。
- **ADR-010（便携数据根）**：重推下发的相对 WorkDir 由 Worker 经 `root.Abs` 解析为本节点绝对路径，与 `CreateInstance` 一致。
- **ADR-020 / ADR-039（节点 enrollment / UUID 身份）**：重推挂在 `onWorkerConnect`（Register 成功或心跳重连后）触发，与注册/身份匹配逻辑解耦——无论走 UUID 重注册、同机 host 兼容还是 token 新建，只要通道就绪即重推。
- **FR-004（节点注册与心跳）**：本 ADR 在其注册/心跳通道之上加「重连后实例重推」，注册与心跳鉴权逻辑不变。
