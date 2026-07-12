# FR-305: 反向隧道单消息尺寸上限统一治理

> 状态：草拟　·　关联 PRD：FR-305　·　分支：dev（单 FR 直落）　·　免新 ADR（ADR-066 双模式架构内的实现约束补全，不改决策）

## 1. 背景与目标

直拨与隧道两条 CP↔Worker 链路的单消息上限**双轨不一致**：

| 方向 | 直拨 | 隧道 |
|---|---|---|
| CP→Worker（请求） | Worker `ServerOptions()` recv=64MiB（5de8778），超限显式 `ResourceExhausted` | grpctunnel 唯一天花板 `math.MaxUint32`（4GB）——FR-304 登记前实测 65MB unary 一路吃进 |
| Worker→CP（响应） | CP 客户端 recv 吃 **4MiB 默认**（`pool.go` 无任何显式 `MaxCall*`，默认漂移活证据） | 同 4GB 无界 |

这既是 ADR-066 双选路（隧道优先/直拨回退）下的**安全边界不一致**（同一载荷换条路行为相反），也是内存暴露面（大 unary 整块缓冲无框架上限）。grpctunnel v0.3.0 不暴露尺寸配置选项——治理手段必须在应用层。

**手段拍板**（登记行三选一）：**应用层拦截器限额**（方案 a）。理由：b（大 RPC 全流式化）——现存大 unary 仅 DeployServerProbe ~7.6MB，远低于 64MiB，YAGNI；c（收边界+文档化 4GB）——放弃一致性，不治本。a 以 ~几十行拦截器让双模式语义完全一致，且**不依赖 grpctunnel/grpchan 的任何选项语义**（双向守卫都落在我们自己的代码里）。

## 2. 需求

- 单一真值上限常量（64MiB，与既有 `maxGRPCRecvMessageBytes` 同值同源），CP/Worker 共享。
- **隧道侧与直拨侧行为一致**：>64MiB 单消息（请求或响应、unary 或 stream 单帧）一律显式 `ResourceExhausted` 拒收，错误信息中文可操作（提示走流式接口/升级）。
- CP 客户端两方向显式化：直拨 dial 加 `MaxCallRecvMsgSize/MaxCallSendMsgSize=64MiB`（修复响应 4MiB 默认暗礁 + 防默认漂移）。
- 流式链路（FR-304 UploadFile / DownloadFile ~64KiB 帧、心跳、终端桥）**零行为变化**。
- 范围外：DeployServerProbe 流式化（<64MiB 载荷保持普通传输，留档）；grpctunnel 4GB 硬上限本身（应用层守卫使其不可达即可）。

## 3. 设计

### 3.1 共享常量与守卫包 `internal/platform/grpcmsg`

```go
const MaxMessageBytes = 64 << 20 // 单一真值：CP/Worker、直拨/隧道、收/发共用

func UnaryServerInterceptor() grpc.UnaryServerInterceptor   // 入参与返回值均 proto.Size 判限
func StreamServerInterceptor() grpc.StreamServerInterceptor // 包 ServerStream 的 RecvMsg/SendMsg 判限
func WrapRegistrar(reg grpc.ServiceRegistrar) grpc.ServiceRegistrar
    // 包装 RegisterService：改写 ServiceDesc 的 Methods[].Handler / Streams[].Handler
    // 使其经上述拦截器执行——grpctunnel 的 ReverseTunnelServer 实现 ServiceRegistrar，
    // 包一层即获得与 grpc.NewServer 选项等效的守卫（不依赖 grpchan 内部语义）
func CallOptions() []grpc.CallOption // MaxCallRecvMsgSize/MaxCallSendMsgSize = MaxMessageBytes
```

- 判限用 `proto.Size`（精确 marshal 字节数）；超限返回 `status.Error(codes.ResourceExhausted, "单条消息 %dMiB 超过上限 64MiB（大文件请走流式接口；如为老版本请升级节点）")`。
- **双向都在拦截器内守**（请求判限于进入 handler 前、响应判限于返回前）——隧道模式下 CP 侧 call options 是否被 grpchan 透传无关紧要，语义由 Worker 侧自守。

### 3.2 Worker 侧接线

- 直拨：`ServerOptions()` 常量改引 `grpcmsg.MaxMessageBytes`（行为不变），**补 `grpc.MaxSendMsgSize`**（响应方向同界，原为 maxInt32 无界）。
- 隧道：`tunnel.go` `serveOnce` 的 `r.register(rts)` → `r.register(grpcmsg.WrapRegistrar(rts))`——WorkerService 全部 RPC 获得与直拨等效的双向守卫。

### 3.3 CP 侧接线

- `pool.go` 直拨 `grpc.NewClient(addr, …)` 追加 `grpc.WithDefaultCallOptions(grpcmsg.CallOptions()...)`——响应上限从 4MiB 默认显式提到 64MiB（修暗礁），请求上限从无界显式收到 64MiB。
- 隧道 channel（`KeyAsChannel`）不包 call options 包装：CP→Worker 请求超限由 Worker 隧道拦截器拒收、Worker→CP 响应超限由 Worker 发送前判限——两方向已闭环，不再引入依赖 grpchan 透传语义的层。

### 3.4 错误语义

两模式、两方向、unary/stream 单帧，超限一律：`ResourceExhausted` + 中文引导。与直拨原生 `ResourceExhausted`（grpc-go 框架产生，英文）在 code 层一致；直拨请求方向仍由框架先拒（框架限已显式同值），拦截器兜底。

## 4. 任务拆分

- [ ] `internal/platform/grpcmsg`：常量 + 双向拦截器 + WrapRegistrar + CallOptions（含单测）
- [ ] Worker：ServerOptions 引常量+补 SendMsgSize；tunnel serveOnce 包 WrapRegistrar
- [ ] CP：pool.go 直拨 dial 加 DefaultCallOptions
- [ ] 测试：拦截器单测（unary 请求/响应超限、stream 帧超限、≤上限放行、恰 64MiB 边界）；进程内反向隧道集成测（复用 FR-281 测试基建）：大 unary 经隧道拒收=直拨同语义、UploadFile 小帧流不受影响；既有 tunnel/pool 测试回归
- [ ] 文档同步：ARCHITECTURE 通信协议节补消息上限统一说明、CHANGELOG 尾行、PRD 状态

## 5. 验收标准

1. **双模式一致**（自动化）：>64MiB unary 经隧道 → `ResourceExhausted`（修前一路吃进）；经直拨 → 同 code；恰 64MiB 两模式同放行。
2. **响应方向**（自动化）：Worker 返回 >64MiB 响应被发送前拒；CP 直拨收 >4MiB 且 ≤64MiB 响应成功（修 4MiB 暗礁的正向证明）。
3. **流式零回归**（自动化+真机）：UploadFile/DownloadFile 帧链路测试全绿；真机 70MiB 流式上传照常成功。
4. **探针部署回归**（真机）：DeployServerProbe ~7.6MB unary 经当前链路照常成功（建实例触发探针注入即验）。
5. 拒收路径真机不强求自然触发（正常业务已无 >64MiB unary——这正是守卫属安全边界的含义），由自动化锁死；真机以 3/4 回归为准，需用户确认。

## 6. 风险 / 待定

- `WrapRegistrar` 改写 ServiceDesc handler 需精确保持 grpc-go handler 签名语义（dec 回调、拦截器链）——单测以真实生成代码的 ServiceDesc 驱动，不手搓假 desc。
- `proto.Size` 对含未知字段的消息与 wire 尺寸可能有极小偏差——用于限额判定（同数量级），非计费，可接受。
- CP 直拨请求方向从无界收到 64MiB：现存最大请求 DeployServerProbe ~7.6MB，余量 8 倍；若未来单请求逼近上限应改流式（spec 留档）。
