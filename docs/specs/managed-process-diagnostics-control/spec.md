# 功能规格：监控页全景观测与受管进程探查处置

> 状态：开发中　·　关联 PRD：FR-406 / FR-407 / FR-408　·　优先级：P1　·　依赖：FR-170、FR-400、FR-401、FR-402

## 1. 背景与目标

左侧导航「观测 → 监控」已指向 `/monitor`，但当前页面仍偏基础指标和 TopN 展示：多指标对比可能初始空态，受管进程只能看摘要，无法按进程深入判断内存泄露、CPU 长占用或 IO 异常，也缺少从观测页直接处置受管异常进程的闭环。

本功能把 `/monitor` 收口为平台观测入口：补齐平台、节点、实例、共享 Bot Worker 与受管进程观测；对受管实例进程树提供只读详细探查和受控处置。平台健康与共享 Bot 区块继续保留在首页 `/`，不新增单独页面；首页「查看全部」兼容跳转到 `/monitor`。

核心边界：**只观测和处置 JianManager 创建、注册或接管的受管资源**。系统不得枚举、展示、诊断或结束任意 OS 进程；PID 级操作必须在 Worker 执行瞬间重新验证目标 PID 仍属于目标实例当前进程树。

## 2. 需求（要什么）

### 2.1 范围内

1. `/monitor` 支持平台、节点、实例三种观测范围，并通过 URL 参数直达：
   - 平台：`/monitor`；
   - 节点：`/monitor?node=<nodeUuid>`；
   - 实例：`/monitor?instance=<instanceUuid>`。
2. 多指标对比默认至少选中一个指标：平台/节点默认 CPU，实例优先 TPS，避免初始空态阻断阅读。
3. 平台范围展示平台健康摘要、异常 TopN、资源归因摘要、共享 Bot Worker 聚合和全局受管进程 TopN。
4. 节点范围展示节点资源、该节点共享 Bot Worker 历史、该节点异常项和受管进程 TopN。
5. 实例范围展示实例指标、世界下钻和目标实例完整受管进程视图。
6. 受管进程详情按 `instanceId + pid` 查询，返回：实例与节点信息、根 PID、目标 PID 是否根进程、父子关系、CPU/RSS/IO、采样时间、运行时长、线程数（可用则返回）、脱敏命令摘要、不可用原因。
7. 进程详情结合 `process_metric_snapshots` 历史样本给出诊断标签和证据，至少覆盖：
   - 样本不足；
   - 采样陈旧；
   - RSS 持续增长（疑似内存泄露）；
   - CPU 持续高占用；
   - IO 写入持续偏高。
8. 根进程处置复用既有实例生命周期：优雅停止、重启、强制终止。
9. 非根子进程处置提供两档：
   - `terminate`：温和终止目标进程；
   - `kill_tree`：强制终止目标进程及其子进程树。
10. 所有破坏性操作必须二次确认，服务端也要求 `confirm=true`，并写审计日志。
11. 前端详情抽屉展示「证据 → 可能原因 → 建议操作」，明确 RSS 是观察值、命令行已脱敏、PID 操作有导致实例崩溃的风险。
12. 页面只在可见时轮询；缺测、陈旧、离线必须显式展示，不以 `0` 冒充缺测值。

### 2.2 不做（范围外）

- 不新增平台健康或共享 Bot 的独立页面。
- 不扫描全机进程、全机端口、任意用户进程或任意 Docker 容器。
- 不展示完整命令行、环境变量、密钥、令牌或进程打开文件列表。
- 不做 JVM heap dump、线程 dump、火焰图或 attach 级深度诊断；本期只做基于现有进程指标的趋势判断。
- 不对单 Bot 虚构 RSS/CPU；Bot 资源仍按共享 Bot Worker 口径展示。
- 不修改实例根进程停机状态机；根进程仍走 `/instances/:id/stop|restart|kill`。

## 3. 设计（怎么做）

### 3.1 观测页面

`apps/control-plane-web/src/pages/MonitoringPage.tsx` 在现有页面上增量改造，不新增路由页面：

- 读取 URL 参数决定初始观测范围；参数缺失时为平台范围。
- `MetricComparePanel` 接收或派生默认指标集合，默认选中一个指标。
- 平台范围消费既有 `observability/overview`、`metrics/resource-attribution`、`metrics/processes/top`、`metrics/bot-runtime`。
- 节点范围按节点 UUID 过滤节点序列、Bot Worker 历史和进程 TopN。
- 实例范围按实例 ID/UUID 过滤实例序列和进程列表。
- 进程表统一展示受管进程条目，并提供详情抽屉入口。

首页 `/` 继续展示平台健康与共享 Bot Worker 区块；首页「查看全部」跳转 `/monitor`，必要时附加 node/instance 参数，不引入新页面。

### 3.2 Worker 受管进程探查

Worker 新增只读 RPC：`InspectManagedProcess`。

执行顺序：

1. 用 `manager.GetState(instance_uuid)` 确认实例存在且 RUNNING。
2. 用 `manager.GetInstancePID(instance_uuid)` 取得当前根 PID。
3. 从根 PID 遍历当前完整子进程树，构造 `pid -> process` 映射。
4. 只在映射内查找目标 PID；不命中时返回 `PID_NOT_MANAGED`。
5. 对命中的目标读取基础指标、父子关系、运行时长、线程数和脱敏命令摘要。
6. 返回 ancestors/children 时只包含同一实例树内的 PID，不泄露树外进程。

命令摘要复用现有脱敏逻辑，避免保存或下发敏感参数。

### 3.3 Worker 受管进程处置

Worker 新增写 RPC：`TerminateManagedProcess`。

安全规则：

- 执行前重复 3.2 的实例状态、根 PID、进程树 membership 验证。
- `pid == rootPID` 时拒绝 PID 级处置，提示走实例 stop/kill。
- `terminate` 只作用于目标 PID：
  - Unix 发送 `SIGTERM`；
  - Windows 若无法提供可靠温和终止语义，返回 `UNSUPPORTED`，不退化成强杀。
- `kill_tree` 强制结束目标 PID 及其子进程树，复用现有跨平台进程树终止能力抽出的公共函数。
- 返回 affectedPids，便于审计与前端刷新；不得跨出目标实例进程树。

### 3.4 Control Plane 服务与 HTTP

HTTP 新增实例子资源端点：

- `GET /api/v1/instances/:id/processes/:pid`
- `POST /api/v1/instances/:id/processes/:pid/actions`

服务层职责：

- 复用实例权限模型，读详情要求可读该实例，写处置要求可操作该实例。
- 根据实例所在节点取得 Worker 连接；节点离线或连接不存在时返回稳定错误。
- 调用 Worker 前后都以实例 ID/UUID 为锚，不接受前端直接指定 node UUID 绕过鉴权。
- 详情接口查询最近历史样本，形成诊断：
  - 诊断窗口：默认最近 30 分钟；若样本少于 3 条输出 `insufficient_samples`。
  - 采样陈旧：最新样本超过 90 秒输出 `stale_samples`。
  - RSS 持续增长：窗口内后半段最小值高于前半段最大值，且增量超过 256MiB 或 20%。
  - CPU 持续高占用：最近连续样本平均 CPU >= 80%。
  - IO 高写入：最近连续样本平均写入 >= 10MiB/s。
- 写操作要求请求体 `confirm=true`；缺失或 false 返回 `CONFIRM_REQUIRED`。
- 审计日志 action：`process.terminate`、`process.kill_tree`；detail 只记录 instanceId、nodeId、pid、mode、affectedPids、结果码，不记录完整命令或环境变量。

### 3.5 前端交互

- 进程表列：实例、PID、名称、CPU、RSS、读写 IO、采样时间、诊断、操作。
- 行内「探查」打开详情抽屉；详情抽屉可刷新。
- 根进程操作区显示优雅停止、重启、强制终止实例，调用既有实例 API。
- 子进程操作区显示温和终止、强制终止子树，调用新增 PID 级 API。
- `kill_tree`、实例强制终止等高风险动作必须使用 `DangerConfirm`，确认文案包含实例名、PID、模式和影响范围。
- 操作成功后刷新：进程 TopN、进程详情、实例列表/详情、指标查询缓存。

## 4. 任务拆分

- [ ] 更新 PRD 索引，登记 FR-406~408。
- [ ] 更新 API/ARCHITECTURE 正式契约。
- [ ] 为 Worker 进程 membership、详情脱敏、根 PID 拒绝、子进程处置写测试。
- [ ] 为 CP 权限、confirm、诊断规则和错误码写测试。
- [ ] 扩展 `proto/worker.proto` 并重新生成 `proto/workerpb`。
- [ ] 实现 Worker `InspectManagedProcess` / `TerminateManagedProcess`。
- [ ] 实现 CP service/router/audit 与 HTTP 响应模型。
- [ ] 实现前端 API hooks、监控页默认指标、进程表和详情抽屉。
- [ ] 补齐 i18n 与 DOM 测试。
- [ ] 运行 Go 与前端相关测试，审查 diff。

## 5. 验收标准

1. `/monitor` 默认有一个指标被选中，平台/节点/实例范围均能展示对应观测内容；`/monitoring` 兼容重定向不破坏。
2. 首页平台健康与共享 Bot 区块仍在 `/`；「查看全部」能跳转到 `/monitor`。
3. 进程详情接口只返回目标实例当前进程树内 PID；非树内 PID、已退出 PID 或非运行实例不会泄露全机信息。
4. 诊断标签必须带窗口、样本数和数值依据；样本不足时明确显示不能判断。
5. 根进程只能走实例生命周期操作；PID 级 API 对根 PID 返回拒绝。
6. 子进程 `terminate` / `kill_tree` 执行前 Worker 二次验证 membership；PID 复用或越界时拒绝。
7. 所有破坏性操作均有前端 DangerConfirm 和后端 `confirm=true` 双保险；取消确认不发请求。
8. 审计日志记录 PID 级操作且不包含完整命令行、环境变量或敏感参数。
9. Go 服务/路由/Worker 测试与前端 DOM 测试通过。
10. 真 CP+Worker 环境中启动受管实例后，监控页能展示该实例进程、打开详情，并对非根子进程执行受控处置；该项需用户或真机环境确认。

## 6. 风险 / 待定

- Windows 的温和终止语义有限；首版允许 `terminate` 返回 `UNSUPPORTED`，不得暗中强杀。
- Docker 策略宿主 PID 映射受 Docker/权限影响；无法证明 PID 属于受管树时必须拒绝 PID 级操作。
- 进程 CPU/RSS 是操作系统观察值，不等同 JVM 内部对象或精确内存泄露证明；前端文案必须避免绝对化判断。
- 子进程被结束可能导致游戏服异常崩溃；必须在 UI 和审计中明确影响范围。
