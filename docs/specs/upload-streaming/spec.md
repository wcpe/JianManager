# 功能规格：文件上传链路流式化

> 状态：开发中（自审通过，代码全绿，待真机验收）　·　关联 PRD：FR-304　·　分支：dev（单 FR 直落）

## 1. 背景与目标

单文件下载 10MiB 静默截断修复（a6a3713，新增 `DownloadFile` 服务端流）时发现的对称隐患：
`POST /api/v1/instances/:id/files/upload` 把 multipart 文件经 `io.ReadAll` 全量读进 CP 内存
（`router/file.go:173`），再经 Worker `WriteFile` unary RPC 一次性发送（`service/file.go:131`，
硬编码 10s 超时）。

复现结论（2026-07-12，bufconn + 生产构型，档案 `.tmp/fr-304-upload-streaming-repro.md`）：

| 传输模式 | 65MB WriteFile 行为 |
|---|---|
| 直拨（`MaxRecvMsgSize=64MiB`） | `ResourceExhausted (68157492 vs. 67108864)` 显式拒收 → 上传 422 |
| 反向隧道（FR-281，grpctunnel） | **无上限直接吞**（分帧 16KB、逻辑消息上限 4GB，`ServerOptions()` 无介入点）→ 双侧内存整块缓冲后成功 |

问题拆解：① 双模式行为不一致（直拨报错、隧道内存暴涨）；② ≤64MB 也双侧整块缓冲；
③ 10s 硬超时使慢链路大文件必超时；④ FR-052/053 插件部署复用 `WriteFile`，批量扇出内存放大。

目标：新增 client-stream `UploadFile` RPC（与 `DownloadFile` 对称），上传任意大小不受
单消息上限约束、CP/Worker 双侧内存恒定、超时随请求生命周期；插件部署路径同步接入；
老 Worker 保留 ≤64MB 兼容回退。

## 2. 需求（要什么）

- 浏览器上传任意大小文件（≤节点磁盘余量）经直拨与隧道均成功，行为一致。
- CP 上传 handler 不再 `io.ReadAll`：multipart 流式读、gRPC 流式转发，单次上传 CP 内存占用 O(chunk)。
- Worker 端流式落盘：临时文件接收 + 完成后原子改名，中途失败不留半截目标文件。
- 上传不再受固定 10s/30s 超时：新链路超时跟随 HTTP 请求 ctx（浏览器不断则不超时）。
- 老 Worker（无 `UploadFile`）兼容：≤64MB 自动回退 `WriteFile` unary；>64MB 明确报错引导升级节点
  （对齐 Download 的「老 Worker 引导升级」话术，绝不静默失败）。
- FR-051 上传覆盖前快照行为保留（挂接点与语义不变，含超 `MaxSizeBytes` 跳过）。
- FR-052/053 插件单发上传与批量扇出部署改走同一流式助手，既有行为与测试语义不变。
- 范围内：proto、Worker `UploadFile` handler、CP `FileService` 流式助手 + 能力探测、
  CP 上传 handler 改造、插件部署接入、前端 `uploadFile` 传参调整、文档同步。
- 不做（范围外）：断点续传 / 分片并行上传（FR-251 客户端分发分块上传是另一域，不合并）；
  上传进度条增强（前端 `uploadFile` 现无 onProgress，不新增）；`ReadFile`/`Write`（在线编辑器）
  链路不动（10MiB 护栏是编辑器语义）；上传大小配额/限流。

## 3. 设计（怎么做）

无新 ADR：遵循 ADR-002（gRPC 节点通信）与既有 `DownloadFile` 服务端流范式的镜像，
不引入新架构模式、不推翻既有决策。

### 3.1 proto（`proto/worker.proto`）

```proto
// UploadFile 单文件分块流式上传（与 DownloadFile 对称，FR-304）。
// 首帧必须携带 instance_uuid + path（content 可同帧携带），后续帧仅 content；
// 零帧即关流约定返回 success=false（无副作用），供 CP 作能力探测。
rpc UploadFile(stream UploadFileChunk) returns (UploadFileResponse);

message UploadFileChunk {
  string instance_uuid = 1; // 仅首帧
  string path = 2;          // 仅首帧，相对工作目录
  bytes content = 3;
}

message UploadFileResponse {
  bool success = 1;
  string error = 2;
  int64 bytes_written = 3; // 实际落盘字节数，CP 与已发送字节数比对校验完整性
}
```

分片大小沿用 `downloadChunkSize` 同值 64KB（新 `uploadChunkSize` 常量，CP 侧切分）。

### 3.2 Worker（`internal/worker/grpc/upload_ops.go`）

- 首帧：解析实例 → `validatePath`（同 `WriteFile`）→ `MkdirAll` → 目标同目录建临时文件
  （`os.CreateTemp(dir, ".jm-upload-*.tmp")`，同卷保证改名原子）。
- 后续帧顺序写临时文件；`io.EOF` 后 close → `os.Rename` 覆盖目标（Windows `MoveFileEx`
  REPLACE_EXISTING 语义支持覆盖已存在文件）→ `SendAndClose(success=true, bytes_written)`。
- 任何错误（含 ctx 取消 / 流中断 / 磁盘满 / 目标被运行中进程锁定改名失败）：删临时文件后返回错误，
  目标文件保持原状——**不存在半截目标文件**。
- 零帧即关流：返回 `success=false, error="缺少首帧"`，不触盘（能力探测约定，写进 proto 注释）。
- 首帧后续帧的 `instance_uuid`/`path` 字段忽略（proto 注释注明）。
- 不设人为大小上限（这正是本 FR 要移除的约束）；并发写同一目标 = 各自临时文件、后改名者胜，
  与现 `WriteFile` 最终一致语义相同。

### 3.3 CP 服务层（`internal/controlplane/service/file.go`）

- 新增 `FileService.UploadFile(ctx, instanceID, path, r io.Reader) error`：
  `validatePath` → 取连接 → 能力探测 → 流式发送（64KB 切分，首帧带元信息）→
  `CloseAndRecv` 校验 `success` 且 `bytes_written == 已发送字节数`。ctx 用调用方请求级 ctx，
  不设固定超时。
- **能力探测与回退**：对每个连接池 `Client` 缓存 `supportsUploadFile`（连接重建自动失效）：
  - 未知时发一次零帧探测流：新 Worker 返回业务错「缺少首帧」→ 支持；`Unimplemented` → 不支持。
  - 支持 → 纯流式，零缓冲。
  - 不支持 → 回退：读 `r` 入内存至 64MB 上限，未超限走既有 `WriteFile` unary（超时放宽为固定
    5 分钟，替代现 10s）；超限直接报「节点 Worker 版本过旧，不支持大文件上传，请先升级节点」。
- 既有 `FileService.WriteFile`（在线编辑器 `/files/write` 用，小文本）保持不动。

### 3.4 CP 路由层（`internal/controlplane/router/file.go` Upload handler）

- 目标路径改从 **query 参数 `?path=`** 取（嵌入式前端与 CP 同二进制发布，无版本错配）；
  兼容既有 form 字段：用 `MultipartReader` 顺序读 part，`path` 字段先于 `file` 出现时亦接受；
  读到 `file` 部分仍无路径 → 400。
- 权限校验、FR-051 `SnapshotBeforeWrite`（在拿到 path 后、开始流式转发前调用，语义不变）
  之后，把 `file` part 的 `io.Reader` 直接交给 `FileService.UploadFile` —— 全程无 `io.ReadAll`。

### 3.5 插件部署接入（`internal/controlplane/service/plugin.go`，FR-052/053）

- `pluginWorkerOps` 窄接口增加 `UploadFile` 流式方法；单发部署与批量扇出的 `WriteFile` 调用点
  改调 `FileService` 同源的流式助手（内容已在 CP 内存/制品库文件中，以 `bytes.Reader`/文件流传入）。
- 回退语义随 3.3 自动生效（老 Worker + ≤64MB 插件照常部署）。
- 每实例部署超时从固定 30s 放宽为 5 分钟（流式后传输时长与 jar 体积相关）。
- 上传入库（sha256 去重）逻辑不动——制品入库本身需要完整内容，属 AssetService 域。

### 3.6 前端（`web/src/api/files.ts`）

- `uploadFile` 改为 `path` 经 query 参数传递，FormData 仅含 `file`（消除 part 顺序依赖）。

## 4. 任务拆分

- [x] proto：`UploadFile` RPC + 消息定义，重新生成 `workerpb`
- [x] 测试先行（Worker）：多帧落盘 / 空文件 / 路径逃逸拒绝 / 零帧探测无副作用 / 中途断流清理临时文件不动目标 / 覆盖已存在文件 / 65MB 直拨（bufconn+`ServerOptions()`）/ 65MB 隧道（grpctunnel 构型）——后两条即本 FR 的回归主证
- [x] Worker：`upload_ops.go` 实现，使上述测试转绿
- [x] 测试先行（CP service）：能力探测 / 老 Worker ≤64MB 回退 WriteFile / 老 Worker >64MB 明确报错 / `bytes_written` 不符报错 / 探测传输错误上抛 / 越界拒绝（fake client）
- [x] CP：`FileService.UploadFile` + 逐次能力探测实现（隧道模式 pool.Get 按取即建 Client，无稳定宿主可缓存，改逐次探测）
- [x] CP：Upload handler 改 `MultipartReader` 流式 + query 参数 path（含 form 字段兼容与 400 分支测试；FR-051 快照挂接回归）
- [x] 插件部署：`pluginWorkerOps` 扩 `UploadFile`、调用点切 `uploadToWorker` 统一入口、既有单测/批量测试适配转绿 + 新增新 Worker 流式部署断言
- [x] 前端：`files.ts` `uploadFile` 改 query 传参，MSW mock handler 同步（query 优先、form 兼容），vitest 相关用例全绿
- [x] 全量验证：`go vet` + `go test ./...` 全绿；`web` `tsc --noEmit` + `eslint` + vitest 1264 全绿（golangci-lint 因本机配置版本不兼容跳过，见风险）
- [x] 文档同步：API.md（upload 端点契约）、ARCHITECTURE.md（gRPC 服务表 + 文件管理章节）、CHANGELOG `[Unreleased]`、PRD FR-304 状态

## 5. 验收标准

1. **大文件双模式一致**（自动化）：65MB 上传经直拨（`ServerOptions()`）与反向隧道（grpctunnel
   生产构型）均成功且落盘内容完整——正是登记前复现里直拨被拒的场景转绿。
2. **CP 零整块缓冲**（代码审查项）：上传链路无 `io.ReadAll`/全量 `[]byte` 中转（老 Worker 回退
   分支除外，且该分支有 64MB 上限）。
3. **Worker 原子落盘**（自动化）：中途断流/失败后目标文件保持原状、无 `.jm-upload-*` 残留；
   成功后内容逐字节一致。
4. **老 Worker 兼容**（自动化，fake `Unimplemented`）：≤64MB 回退 `WriteFile` 成功；
   >64MB 返回含「升级节点」引导的明确错误。
5. **FR-051 快照不回归**（自动化）：上传覆盖已存在文件前仍产生改前版本；超 `MaxSizeBytes` 跳过。
6. **FR-052/053 不回归**（自动化）：插件单发/批量部署既有测试全绿。
7. **超时行为**（代码审查项）：新链路无固定短超时，随请求 ctx；老 Worker 回退分支 5 分钟。
8. **真机维度（需用户确认通过，不由测试绿替代）**：FR-277 主机真浏览器上传 >64MB 文件到实例，
   成功且 sha256 与本地一致；隧道模式节点重复同一验证。
9. 全量质量门：Go 测试/vet/lint 绿，web tsc/lint/vitest 绿。

## 6. 风险 / 待定

- **零帧探测契约**：靠「零帧→业务错、`Unimplemented`→老版本」区分能力，属私有 wire 约定，
  已写进 proto 注释固化；若未来引入正式能力协商（如 GetVersion 扩 features），此探测可平滑替换。
- **Windows 目标文件被锁**：运行中游戏服锁住目标（如 server.jar）时 `os.Rename` 失败——报错并
  清理临时文件，目标不受损；与现 `WriteFile` 遇锁直接报错行为等价，不在本 FR 做绕锁。
- **回退分支阈值**：老 Worker 回退上限取 64MB（= `MaxRecvMsgSize`，protobuf 编组开销留由
  gRPC 拒收兜底并同样话术报错）；是否需要更保守（如 60MB 提前拒）留实现时按报错可读性定夺。
- **批量扇出并发内存**：流式后每实例并发流各持 64KB 缓冲 + 制品文件盘读，内存放大问题随本 FR 消除；
  扇出并发度沿用既有实现不另调。
