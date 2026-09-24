# 功能规格：VictoriaLogs 资产分发与 Worker 运行时（FR-475）

> 状态：实现中（Worker supervisor、CP runtime status/control RPC 和节点管理面已接线；远程 HOT/COLD/Rehydrate、资产 hash 校验与 RustFS 探测通过；预算采样已落地（`vlsup.EvaluateBudget` + RSS/磁盘采样、Worker 周期采样日志）；CP 资产下载闭环与真机预算降级证据仍待验）　·　关联 PRD：FR-475　·　依赖：FR-472

## 1. 背景与目标

在 Worker 受管运行 VL，隔离进程资源与失败日志。

本规格不得重新定义 FR-472 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：FR-475 资产审批已批准 VL v1.52.0 双平台包/解包 hash、Apache-2.0 许可证和 182 个 Go module 清单；其他版本须重跑受影响能力验证。
- 范围内：CP 只分发审批 tag、哈希和许可清单；Worker supervisor 按 namespace 管 HOT/COLD/Rehydrate，localhost+本地鉴权，健康与分区恢复分离；失败日志走独立 sink，禁止递归。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

CP 只分发审批 tag、哈希和许可清单；Worker supervisor 按 namespace 管 HOT/COLD/Rehydrate，localhost+本地鉴权，健康与分区恢复分离；失败日志走独立 sink，禁止递归。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-472 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-472 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-482 文档对账。

## 5. 验收标准

- [ ] Linux/Windows 包校验、解包校验、Apache-2.0 记录、优雅停机、预算/RSS、进程重启和无 VL 降级均有实验证据。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-472 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-472 已冻结；本规格仍须完成 CP 资产下载闭环、真机预算降级证据和 Runbook C 故障矩阵，未完成前保持开发中。预算采样与阈值评估（磁盘 80/90、RSS 超限判定）已在 `vlsup` 落地，真机数值取证属 Runbook C。
- v1.52.0 tag、双平台包/解包 hash、Apache-2.0 许可、构建日期和依赖清单见受控文档 [`asset-inventory.md`](asset-inventory.md)（代码真源 `internal/platform/logasset/approved.go`），作为当前资产基线与安装校验依据。

## 3.1 资产、进程与安全契约

- FR-475 资产清单必须登记审批 tag、下载包 SHA-256、解包后可执行文件 SHA-256、Apache-2.0 许可、linux-amd64/windows-amd64 兼容测试和构建日期；任一校验不匹配不得安装。
- Worker supervisor 分别管理 HOT、COLD、Rehydrate 实例；VL 进程 health、分区恢复完成和查询完整可用是不同状态。
- 受管 VL 启动是异步的：接线 ingest/查询前必须等待实例就绪（`vlsup.Supervisor.WaitHealthy`），否则启动期 VL 写连接被拒会令采集运行时创建失败（FR-475 启动时序，见 CHANGELOG 修复）。
- VL 仅监听 localhost，使用独立本地鉴权密钥和受管数据根；CP/Worker 对 Search、Stats、Fields、Facets、Tail、Rehydrate、Export 均执行 scope 校验。
- HOT cache 预算不等于 RSS 上限；supervisor 必须执行 Worker 级日志总预算和各子预算，超过预算进入降级。
- 受管 VL 进程注入 `GOMEMLIMIT`（默认 512MiB，`log_vl.memory_limit_bytes` 可覆盖，负值不注入）：`-memory.allowedBytes` 只约束 cache，不约束 Go heap；真机复测该上限把 VL RSS 由 ≈1.1GiB 压到 ≈131MiB（契约 §6.6 RSS ≤ 1GiB 达标）。
- VL 写失败日志必须走独立、限流且不回写 VL 的 sink，防止错误递归。
