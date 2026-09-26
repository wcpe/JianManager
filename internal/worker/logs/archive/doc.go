// Package archive 实现 FR-478 Deep Archive 与 Rehydrate 基础包（纯逻辑 + Provider 抽象）。
//
// 契约来源：
//   - docs/specs/worker-log-deep-archive/spec.md §3.1 Archive 与 Rehydrate 契约
//   - docs/specs/worker-log-platform-contract/spec.md §4.3 受管恢复分段、§5 Catalog generation
//
// 关键不变量（不得在本包外另立契约）：
//
//  1. 分区键为 storage_namespace + UTC 日 + generation，不是每实例每天一个天然分区；
//  2. Manifest 固定 schema_version / parser_version / engine_version / generation、
//     内容校验和、时间范围、coverage 与 raw_files[]；
//  3. 受管 Raw Archive 是「复制 + 校验 + 登记」成功后的恢复来源；实例目录中的 .gz
//     在登记前只是 source，不是 archive；STDIO_PRIMARY 必须由 Worker 生成受管 Raw 分段；
//  4. Rehydrate 任务具备 task_id、租约、合并键、取消、超时、磁盘预留与 generation 隔离；
//     恢复不改变原始 _time；同一合并键的并发请求复用在途任务；
//  5. 清理 hold 与 Query View 租约互相可见：任一 hold 未解除时不得清理对应 generation 分区；
//  6. 幂等重归档；对象损坏可重试；禁止同路径不同 generation 的盲覆盖。
//
// 本包不实现真实 S3/MinIO 网络客户端（Provider 接口一致，单测使用内存后端），
// 不访问 CP 业务库，不把物理目录扫描结果当作归档权威。
package archive
