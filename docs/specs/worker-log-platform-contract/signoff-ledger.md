# FR-473～484 交付判定台账（供 Gate-4 签字）

> 用途：本文件把每个 FR 的**验收证据**与**未覆盖缺口**并列，供用户逐条签字或打回。
> 按 `.claude/rules/gate-merge.md`，FR 验收须由用户确认，**Agent 不得自行标记 done**。
> 编制日期：2026-09-24　·　分支：`feature/fr-log-platform-foundation`（工作区干净，CI 9/9 全绿）。
> **签署基线以 PR #16 的最新 head 为准**（本台账会随修订产生新提交，故不在此钉死 commit hash）。

## 判定图例

| 标记 | 含义 |
|---|---|
| ✅ 可签 | 规格验收标准有对应证据，且证据为真机/真浏览器级（非仅单测） |
| ⚠ 有缺口 | 主体已验收，但规格明确列出的某条未覆盖 → 需决定「接受为已知边界」还是「打回补齐」 |
| ⛔ 未就绪 | 关键路径仍为占位或规格自述未完成 |

## 逐条台账

| FR | 判定 | 已有证据 | 缺口 / 待你决定 |
|---|---|---|---|
| FR-473 | ✅ 可签 | Shared Contracts 冻结、ADR-094 accepted、proto 契约冻结 | 契约类 FR，交付物即契约本身；PRD 自述「不等于已交付」指其下游实现 |
| FR-474 | ✅ 可签 | 生产采集/投影/ACK 恢复/reclaim 通过；**Runbook A 真机 `kill -9` 重启通过**；磁盘容量**真实填盘验收通过**（容器内受限 tmpfs `--tmpfs /data:size=48m`：真实越过 80%→`DEGRADED_STORAGE`、91.67%→`PAUSED` 必记缺口；经真实 `Pipeline.Ingest` 落账本 `AcquirePaused`+缺口+未投递；**真实 ENOSPC** 填至 100% 观测 `no space left on device`）。证据 `worker-log-acquisition/acceptance-real.md` | 无 |
| FR-475 | ✅ 可签 | **真机验收通过**（真 VL v1.52.0）：Multiline 5 行归并为 1 事件且保留 `Caused by`、跨午夜回拨正确（未猜成未来）、半条事件被 Flush 闭合、账本 ready；**修复损坏编码致 hash 与 VL 正文不一致的缺陷**（净化 + `encoding_sanitized` 审计标记，VL 侧重算 hash 一致）；7 条新回归 + 2 项变异验证。证据 `worker-log-normalizer/acceptance-real.md` | 无（规格 §5 四类边界已全部覆盖） |
| FR-476 | ✅ 可签 | v1.52.0 资产审批、supervisor/CP runtime 面、远程三实例、预算采样与 80/90 降级、**CP 资产上传→签名下载→Worker 安装真机闭环**、GOMEMLIMIT 调优；**Windows 包校验与解包已补真机证据**（真实包经 asset API 下载：双层哈希与审批值一致、`InstallApprovedPackage` 真实 zip 解包 PASS、CP `Cache`/`Open` 校验通过并拒绝篡改）。证据 `worker-victorialogs-runtime/acceptance-windows.md` | **未覆盖的仅是「Windows 主机实机部署验证」**。经差异面盘点：`vlsup` 的 Windows 特有分支**仅 1 处**（zip 格式选择，已验证），其余（优雅停机编排/预算/重启/无 VL 降级）为平台无关逻辑且有 25 例测试覆盖，进程停止信号亦已按平台差异处理（`exec.go` Interrupt + grace + Kill 兜底）→ 不是逻辑未覆盖，而是实机执行未做（本机无 Wine/qemu，容器共享宿主内核）。**用户已裁决接受为已知边界**（2026-09-25）|

> **勘误（2026-09-24）**：本台账曾把 FR-476 登记为「外部阻塞·本机网络不可达 GitHub」，**该判断有误**。
> 我只测了 `github.com` 的 releases 下载端点（超时）就下了结论；实际 `api.github.com` 可达，
> 经 asset API 端点即可取得该包（详见 `acceptance-windows.md` §1）。
> 教训：判定网络不可达必须测足多个端点。
| FR-477 | ✅ 可签 | 状态机单测绿；**Runbook B 真机迁移链通过**（FROZEN→…→CLEANED、物理迁移、HOT 空/COLD 持有）、逐状态崩溃恢复视图；规格 §5 关键不变量均有直接测试：`TestInvariant_ReattachedOldDirExcludedFromQueryPlanner`（物理存在≠查询权威，崩溃恢复后同样排除）、last-copy 保护 `lifecycle_test.go:459`（未确认接收前拒绝 detach，断言 `BlockedReason` 含 "last-copy"） | 规格头原写「Runbook B 真机待验」为**漂移，已修** |
| FR-478 | ✅ 可签 | **真机 RustFS S3 验收通过**：登记/幂等/manifest/对象往返/Rehydrate 租约+同键复用+预留拒绝+完成、发布 generation 清理矩阵 | — |
| FR-479 | ✅ 可签 | 全查询 RPC 已接线；远程 Search/Stats/Facets/Export 通过；**CP 联邦真机查询通过** | — |
| FR-480 | ✅ 可签 | logcoord+HTTP+生产 Assemble；**真机联邦查询与延迟取证**（串行 p95 28.5ms、8 并发 p95 15.2ms） | — |
| FR-481 | ✅ 可签 | cutover+Legacy+DualPath 地基、HTTP 管理面已注册（默认关闭）、单测覆盖；**跨切换查询由 `/logs/legacy` + `/logs/federation` 两条真实路径承担**，其中 `/logs/federation` **已真机验收通过**（`coverage.complete=true`、返回真实事件、契约复合排序；见 `acceptance-record.md` §6） | 无。`DualPath` 的 federated 侧保持 `UnimplementedFederatedQuerier` 是**有意设计**（PRD 已记「由 `/logs/federation` 承担，保留 API 不加投机接线」），确认无生产调用方，非欠账 |
| FR-482 | ✅ 可签 | **真浏览器验收通过**（真实联邦列表/Stats·Facets 一致、失败态引导；修了 Stats `agg.count` 读法缺陷）；失败态 DOM 矩阵 19 项。**本轮修复真实缺陷**：联邦非 404 失败（401/代理 5xx/无响应）时未降级 → `federationActive` 误判为 true → 以空联邦数据为准，**列表永久空白**而经典 `/logs` 数据已就绪（生产影响：Worker 隧道不可达时用户见空白页而非降级数据）。现 404/401/403/5xx/无响应一律降级，400/422/429 等业务错误不降级（保留真实错误语义）；真实浏览器复验 logs-virtual=true / data-total-count=12004 / 渲染 24 行，并补 `federation-availability.test.ts` 固化四类判定 | — |
| FR-483 | ✅ 可签 | 受控文档已补：`asset-inventory.md`、`recovery-manual.md`、`acceptance-record.md`；已对账 ARCHITECTURE/API/CHANGELOG/PRD/规格状态 | 文档类 FR |
| FR-484 | ✅ 可签 | **真机验收通过**（node-main + 真 VL v1.52.0）：64/128/150 源稳态 22.5/22.3/20.0MiB、峰值 56.0/62.0/61.8MiB；**342 源逐一核对 1 026 000 条事件零丢失零重复**；五个门禁回归 + 四条累积结构回归（均含变异验证）；受控证据 `worker-log-canonical-eventstore/acceptance-real.md` | — |

## 汇总

- **可签（12 条，全部）**：FR-473、FR-474、FR-475、FR-476、FR-477、FR-478、FR-479、FR-480、FR-481、FR-482、FR-483、FR-484
- **FR-476 用户裁决（2026-09-25）**：**接受「Windows 主机运行时行为」为已知边界**。依据：资产审批、supervisor/CP runtime 面、预算采样与降级、CP→Worker 真机闭环、Windows 包双层哈希与 zip 解包均已有真机证据（真实 6 127 882 字节包经生产代码路径验证）；差异面盘点为 `vlsup` 的 Windows 特有分支仅 1 处（zip 格式选择，已由 `TestInstallPackageVerifiesBothHashesAndPublishesVersionDirectory/windows` 子测试覆盖），其余为平台无关逻辑且有 25 例测试覆盖（`supervisor_test.go` 18 + `budget_test.go` 7），进程停止信号亦按平台差异处理（`exec.go` Interrupt + grace + Kill）。本机无 Wine/qemu，属「实机执行未做」而非「逻辑未覆盖」。

> 编号提示：本批 FR 经两次让路重编号（FR-433～444 → FR-472～483 → **FR-473～484**）。
> 若你此前记录的是 FR-472～483，请按 **+1** 对应（例如此前的 FR-481 = 现在的 FR-482）。

> **勘误（2026-09-24）**：本台账初版把 FR-481 判为「⛔ 未就绪·federated 查询仍为占位」，**属误判**。
> 核查后确认：`UnimplementedFederatedQuerier` 是有意保留的占位（`DualPath` federated 侧无任何调用方），
> 跨切换查询由 `/logs/legacy` + `/logs/federation` 承担，后者已真机验收。误判源于把「某接口的一侧是占位」
> 直接等同于「功能未交付」，未先核实该路径是否可达、是否有意为之。

## 待你决定的事

**仅剩 1 项**：**FR-476 的 Windows 运行时行为**是否接受为已知边界。

本轮已补上 Windows 的**分发与安装侧**真机证据（详见 `worker-victorialogs-runtime/acceptance-windows.md`）：
经 GitHub asset API 取得真实 Windows 包 → 双层哈希与审批值一致 → `InstallApprovedPackage` 真实 zip 解包通过
→ CP `Cache`/`Open` 校验通过且拒绝篡改。

**仍未覆盖**：Windows 上的实际运行行为（进程启动、优雅停机、预算/RSS 采样、进程重启、无 VL 降级）
——需在 Windows 主机执行 `.exe`；本机 linux-amd64 与容器（共享宿主内核）均无法运行 Windows PE。

→ 请裁决：**接受该边界**（分发与安装侧已验收，运行时行为留待 Windows 机器），或**提供一台 Windows 机器**继续补测。

> 另：FR-484 的 PRD 状态目前写「🔨 开发中·真机验收通过」。**在你签字前，我不会把它改成「✅ 已交付」**（尚无版本号，且 gate-merge 要求用户确认）。
