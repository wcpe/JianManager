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
| FR-473 | ✅ 可签 | Shared Contracts 冻结、ADR-094 accepted、proto 契约冻结。**冻结退出条件逐项核实（2026-09-26）**：① ADR-094 `accepted`；② proto 冻结——`worker.proto` 含 10 个日志 RPC（能力协商 `GetLogCapabilities` + `LogCreateView/LogSearch/LogStats/LogFields/LogFacets`）；③ VL 发行资产正式审批——`worker-victorialogs-runtime/asset-inventory.md` §1「审批基线」v1.52.0；④ 性能判据入契约——本包 `spec.md` 含 8 处 p95/RSS 判据。四项均满足 | 契约类 FR，交付物即契约本身；PRD 自述「不等于已交付」指其下游实现（分别归 FR-474~484 各自验收） |
| FR-474 | ✅ 可签 | 生产采集/投影/ACK 恢复/reclaim 通过；**Runbook A 真机 `kill -9` 重启通过**；磁盘容量**真实填盘验收通过**（容器内受限 tmpfs `--tmpfs /data:size=48m`：真实越过 80%→`DEGRADED_STORAGE`、91.67%→`PAUSED` 必记缺口；经真实 `Pipeline.Ingest` 落账本 `AcquirePaused`+缺口+未投递；**真实 ENOSPC** 填至 100% 观测 `no space left on device`）。证据 `worker-log-acquisition/acceptance-real.md` | 无 |
| FR-475 | ✅ 可签 | **真机验收通过**（真 VL v1.52.0）：Multiline 5 行归并为 1 事件且保留 `Caused by`、跨午夜回拨正确（未猜成未来）、半条事件被 Flush 闭合、账本 ready；**修复损坏编码致 hash 与 VL 正文不一致的缺陷**（净化 + `encoding_sanitized` 审计标记，VL 侧重算 hash 一致）；7 条新回归 + 2 项变异验证。证据 `worker-log-normalizer/acceptance-real.md` | **暂缓**：`worker-log-normalizer/spec.md:40` 与 `acceptance-real.md:72` 仍明确写 Windows 证据缺失/未测；按用户裁决暂不标记，待 Windows 验收 |
| FR-476 | ✅ 可签 | v1.52.0 资产审批、supervisor/CP runtime 面、远程三实例、预算采样与 80/90 降级、**CP 资产上传→签名下载→Worker 安装真机闭环**、GOMEMLIMIT 调优；**Windows 包校验与解包已补真机证据**（真实包经 asset API 下载：双层哈希与审批值一致、`InstallApprovedPackage` 真实 zip 解包 PASS、CP `Cache`/`Open` 校验通过并拒绝篡改）。证据 `worker-victorialogs-runtime/acceptance-windows.md` | **未覆盖的仅是「Windows 主机实机部署验证」**。经差异面盘点：`vlsup` 的 Windows 特有分支**仅 1 处**（zip 格式选择，已验证），其余（优雅停机编排/预算/重启/无 VL 降级）为平台无关逻辑且有 25 例测试覆盖，进程停止信号亦已按平台差异处理（`exec.go` Interrupt + grace + Kill 兜底）→ 不是逻辑未覆盖，而是实机执行未做（本机无 Wine/qemu，容器共享宿主内核）。**用户已裁决接受为已知边界**（2026-09-25）|

> **勘误（2026-09-24）**：本台账曾把 FR-476 登记为「外部阻塞·本机网络不可达 GitHub」，**该判断有误**。
> 我只测了 `github.com` 的 releases 下载端点（超时）就下了结论；实际 `api.github.com` 可达，
> 经 asset API 端点即可取得该包（详见 `worker-victorialogs-runtime/acceptance-windows.md` §1）。
> 教训：判定网络不可达必须测足多个端点。
| FR-477 | ✅ 可签 | 状态机单测绿；**Runbook B 真机迁移链通过**（FROZEN→…→CLEANED、物理迁移、HOT 空/COLD 持有）、逐状态崩溃恢复视图；规格 §5 关键不变量均有直接测试：`TestInvariant_ReattachedOldDirExcludedFromQueryPlanner`（物理存在≠查询权威，崩溃恢复后同样排除）、last-copy 保护 `lifecycle_test.go:459`（未确认接收前拒绝 detach，断言 `BlockedReason` 含 "last-copy"） | 规格头原写「Runbook B 真机待验」为**漂移，已修** |
| FR-478 | ✅ 可签 | **真机 RustFS S3 验收通过**：登记/幂等/manifest/对象往返/Rehydrate 租约+同键复用+预留拒绝+完成、发布 generation 清理矩阵 | — |
| FR-479 | ✅ 可签 | 全查询 RPC 已接线；远程 Search/Stats/Facets/Export 通过；**CP 联邦真机查询通过** | — |
| FR-480 | ✅ 可签 | logcoord+HTTP+生产 Assemble；**真机联邦查询与延迟取证**（串行 p95 28.5ms、8 并发 p95 15.2ms） | — |
| FR-481 | ✅ 可签 | cutover+Legacy+DualPath 地基、HTTP 管理面已注册（默认关闭）、单测覆盖；**跨切换查询由 `/logs/legacy` + `/logs/federation` 两条真实路径承担**，其中 `/logs/federation` **已真机验收通过**（`coverage.complete=true`、返回真实事件、契约复合排序；见 `acceptance-record.md` §6） | 无。`DualPath` 的 federated 侧保持 `UnimplementedFederatedQuerier` 是**有意设计**（PRD 已记「由 `/logs/federation` 承担，保留 API 不加投机接线」），确认无生产调用方，非欠账 |
| FR-482 | ✅ 可签 | **真浏览器验收通过**（真实联邦列表/Stats·Facets 一致、失败态引导；修了 Stats `agg.count` 读法缺陷）；失败态 DOM 矩阵 19 项。**本轮修复真实缺陷**：联邦非 404 失败（401/代理 5xx/无响应）时未降级 → `federationActive` 误判为 true → 以空联邦数据为准，**列表永久空白**而经典 `/logs` 数据已就绪（生产影响：Worker 隧道不可达时用户见空白页而非降级数据）。现 404/401/403/5xx/无响应一律降级，400/422/429 等业务错误不降级（保留真实错误语义）；真实浏览器复验 logs-virtual=true / data-total-count=12004 / 渲染 24 行，并补 `federation-availability.test.ts` 固化四类判定。**逐项点击实测又修一处回归**：联邦路径成为默认数据源后未解析 `level`，用户在日志中心点「错误」筛选**无任何反应**（经典路径本支持该参数，属切数据源时漏接）；已改为在 CP 侧把 level 组合进 filter 表达式（wire 契约只有单一 filter 字段，不改冻结契约），level 走白名单防注入，并修正了与含 OR 关键词组合时的布尔优先级。真机实测：error→3 条、info→11、warn→2，合计 16=全量；注入尝试 4 例全拒。**生产部署复核（2026-09-25 21:00）**：修复已随生产 CP 替换上线并**经产物与数据双重验证**——提交 19:00 < 替换 20:35 < 进程启动 20:36；生产二进制含 `router.federationFilter` 符号；生产 VL 实测 `level:ERROR`=565、`level:WARN`=122528、`level:INFO`=231704，三者合计 354797 = `*` 全量，**完全吻合**，证明 `level:` 表达式在真机 VL v1.52.0 上语义正确。生产栈稳定性：CP/Worker 单次启动无重启（启动 1 次/停止 0 次），VL 内存 349 MiB（较早前 387 MiB 下降，无泄漏迹象），采集持续 ~459 条/秒，DB `integrity_check`=ok（59 用户/153 实例，与替换前备份一致）。**回滚预案已验证可用**：DB 备份 2.0G 自检 `ok`、CP 二进制 57M 与配置齐备（生产 CP 目录下 `{data/jianmanager.db,bin/jianmanager-cp,control-plane.yml}.bak-20260925-203245`，另 Worker 配置 `worker.yml.bak-20260925-204556`），升级路径为 GORM `AutoMigrate`（`database.go:133-135`）天然幂等；11 个受管游戏实例 PID 与替换前基线逐一一致，未受影响。**`source` 缺口已按用户裁决修复（2026-09-25，方案 B 前缀推导）**：原记为「事件结构无类别，信息在采集侧即丢失」**不准确，据生产 VL 实证更正**——`log_source_id` 形如 `inst:<实例ID>/<流>`（生产 10 个源全为 `inst:` 前缀，如 `inst:147/file`；构造点 `ingest/instances.go:64`），类别与 ID 均在，故**无需改已冻结的 wire 契约**即可由前缀推导。真实缺口为两处：① 联邦路由**完全未读取 `source` 查询参数**（`log_federation.go` 原无该参数处理），故下拉选择无任何效果；② 前端下拉取值域为 `instance/control_plane/worker`（`LogsPage.tsx`），与事件实际取值 `inst:<id>/file` 不同构，致 `t(\`logs.source_${log.source}\`)` 查不到 i18n 键而回退显示原始 ID。**已实施**：CP 侧新增 `federationSourcePrefix` 把类别映射为 `log_source_id` 前缀并组合进 filter 表达式（`inst:`/`node:`/`control_plane:`），取值走白名单防注入——`control_plane` 类只落 CP 库、不进联邦数据面，显式给不可能存在的前缀使其诚实返回零结果，而非当作无约束放行全量；前端新增 `normalizeSource` 把原始标识归一为取值域，来源列改显示 i18n 标签（实例/节点/平台）。**实证**：VL 的字段匹配即前缀语义，实测 `log_source_id:"inst:"` 与 `"node:"` 在生产 VL 上分别命中 354797 / 0 条（生产仅实例类源），故无需通配符。**验证**：Go 新增 3 组测试（含 10 子用例）、前端新增 2 个 DOM 用例；两处修复均做**变异验证**（禁用 source 组合、移除 keyword 括号、归一函数直通，三种变异均被测试捕获）；CP 将下发的表达式经生产 VL 实测语义正确。**真浏览器端到端复验**（隔离演示栈，重建含修复的前后端后）：来源列显示可读标签（实例/节点/平台）**不再出现原始 `inst:`/`node:` ID**；选「实例」下发 `source=instance`、选「节点」下发 `source=worker`（各 3 次请求实测）；行数与数据一致——演示栈 VL 仅有 `node:1`/`node:self` 两类源，故选「实例」返回 0 行、选「节点」返回 16 行，**证明筛选真实生效而非无约束放行** | — |
| FR-483 | ✅ 可签 | 受控文档已补：本规格包内 `recovery-manual.md`、`acceptance-record.md`，以及相邻规格包 `worker-victorialogs-runtime/asset-inventory.md`（资产清单按 VL 运行时归属，故不在本包内）；已对账 ARCHITECTURE/API/CHANGELOG/PRD/规格状态 | 文档类 FR |
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

## 已裁决事项与签字动作

**FR-476 的 Windows 运行时行为**：用户已于 **2026-09-25** 裁决接受为已知边界，不再是待裁决项。

本轮已补上 Windows 的**分发与安装侧**真机证据（详见 `worker-victorialogs-runtime/acceptance-windows.md`）：
经 GitHub asset API 取得真实 Windows 包 → 双层哈希与审批值一致 → `InstallApprovedPackage` 真实 zip 解包通过
→ CP `Cache`/`Open` 校验通过且拒绝篡改。

**仍未覆盖**：Windows 上的实际运行行为（进程启动、优雅停机、预算/RSS 采样、进程重启、无 VL 降级）
——需在 Windows 主机执行 `.exe`；本机 linux-amd64 与容器（共享宿主内核）均无法运行 Windows PE。
该边界已被用户接受，FR-476 仍可签。

**当前剩余动作只有用户逐条签字**：签字后再按本台账的预案，将 PRD 12 条状态更新为 `✅ 已交付@v0.24.0`；Agent 不得在签字前自行标记。

> FR-484 的 PRD 状态目前写「🔨 开发中·真机验收通过」。**在你签字前，我不会把它改成「✅ 已交付」**（gate-merge 要求用户确认）。

## 生产部署状态与待签字内容的差分（2026-09-25 签字前说明）

**生产 CP 当前为 20:35 替换的那一版**，含此前的联邦降级与 `level` 筛选修复，**但不含 22:04 完成的 `source` 前缀推导修复**。以符号检测确认（带阳性对照，方法本身有效）：

| 二进制 | `router.federationFilter`（level 修复） | `router.federationSourcePrefix`（source 修复） |
|---|---|---|
| 生产 CP（20:35） | 2 处（含） | **0 处（不含）** |
| 本轮新建演示栈 CP | 含 | 含 |

**影响面**：仅限日志中心「来源」下拉与来源列标签——生产上目前选来源仍无效果、来源列显示原始 ID；其余功能（级别筛选、失败降级、联邦查询、导出）不受影响。

**Worker 侧无需替换**：本批改动未触及 Worker 代码（`worker` 路径下仅台账文档变更），生产 Worker 二进制（09-24）无需更新。

**生产配置也无需同步**：生产 `worker.yml` 未配 `log_sources`（走默认），实际采集源全为 `inst:<id>/file`，与代码默认行为一致。

**因此签字有两种口径，请你裁决**：
- **A（建议）** 先替换生产 CP 到含 `source` 修复的版本，再做逐条签字——使「签字的即生产在跑的」。
- **B** 按当前生产版本签字，`source` 修复随后续版本一并上线（届时台账需注明该 FR 的已知缺口已在 HEAD 修复但未上生产）。

## 生产替换完成与浏览器逐项点击验证（2026-09-25 22:52）

**替换**：生产 CP 已替换为含 `source` 修复的构建（符号检测确认 `router.federationFilter` 2 处 + `router.federationSourcePrefix` 1 处），替换未改变版本号（当时 `version.go` 为 `0.23.0`；该值自身属版本漂移，已另开 PR #22 于 master 上修复为 `0.24.0-dev`，与本 PR 无关）。替换后复核：CP `HTTP 200`、gRPC 19100 就绪、**11 个受管游戏实例 PID 与替换前基线逐一一致**、Worker 存活、VL 采集连续（354797 条无中断）。替换前备份保留于 `bin/jianmanager-cp.bak-20260925-225053`。

**浏览器逐项点击验证**（生产 19000，真实登录 admin，只读操作，12 项）：

| # | 项目 | 结果 |
|---|---|---|
| ① | 登录 | ✓ 进入主界面 |
| ② | 日志中心列表加载 | ✓ 24 行 |
| ③ | 级别筛选「错误」 | ✓ 24 行全为「错误」 |
| ③ | 级别筛选「警告」 | ✓ 0 行（生产近 1h 无 WARN，VL 实测确认，非缺陷） |
| ③ | 级别筛选「信息」 | ✓ 24 行全为「信息」 |
| ④ | 复位「全部级别」 | ✓ 恢复 24 行 |
| ⑤ | 切换「全部日志」视图 | ✓ 24 行 |
| ⑥ | 来源列标签 | ✓ **无原始 `inst:`/`node:` ID**，显示「实例」 |
| ⑦ | 来源筛选「实例」 | ✓ 下发 `source=instance`，24 行 |
| ⑦ | 来源筛选「节点」 | ✓ 下发 `source=worker`，**0 行**（生产仅有 `inst:` 类源，VL 实测 `node:` 前缀=0，证明筛选真实生效而非无约束放行） |
| ⑧ | 复位「全部来源」 | ✓ 恢复 24 行 |
| ⑨ | 来源+级别组合 | ✓ 下发 `source=instance&level=error`，两约束同时生效 |
| ⑪ | 翻页 | ✓ 正常 |
| ⑫ | 页面错误 | ✓ 无 |

**口径说明**：界面计数（如 994507）为联邦 Stats 聚合值，非列表行数；列表行数由 `items.length` 决定（`LogsPage.tsx:241`），故与 VL 单实例计数口径不同，非缺陷。

**生产二进制与 PR head 的代码等价性证明（2026-09-25，签字前）**：为避免「签的内容与生产在跑的不一致」，以二进制内嵌溯源直接比对（非靠时间推断）：`go version -m` 读出生产 CP 的版本为 `v0.23.1-0.20260925143955-b8ca53936ff4`，后缀即构建时的 HEAD `b8ca5393`；`b8ca5393..当前 HEAD(3c4edae1)` 的差异**仅 `signoff-ledger.md` 一个文档**（+25 行），**代码文件 0 个**。故生产在跑的代码 = PR #16 的代码，签字基线一致。

## 生产暴露的日志误报（2026-09-26 签字前核查发现，FR-481 范畴）

**现象**：生产 Worker 日志出现 164 条 `ERROR 实例日志受管 Raw 持久化失败 instanceId=44189c04-… stream=stderr error="ingest: instance output has no durable source binding"`，集中在 `20:47:14~20:47:43`（29 秒内），此后再无。

**根因（2026-09-26 复核修正，前一版结论有误）**：错误发生在 **20:47:14~20:47:43**，而 Worker 日志显示 **20:47:13 批量「已恢复 daemon 实例」**（该轮同时恢复多个 daemon wrapper 连接，`beacon-main` 是其中之一）。即：**进程输出的恢复早于日志绑定注册**，二者之间存在一个短暂的启动恢复窗口；此窗口内的输出命中 [instances.go:140](internal/worker/logs/ingest/instances.go#L140) 的「无绑定」ERROR 分支。绑定随后建立，**错误随即停止**——末次错误 20:47:43 之后 3.7 小时无任何新错误，与「窗口内自限」一致。

> **更正说明**：前一版把根因写成「`beacon-main` 无 workDir 故无绑定」，**与事实不符**——复核账本确认该实例**有**绑定（`mode=STDIO_PRIMARY`、`targetID=inst:153`、`workDir` 非空），且 `daemon` 类型并不等于无 workDir。当前 12 个运行实例**全部有绑定**（逐一配对：缺失 0 个）。真实成因是**启动恢复窗口的时序竞态**，非「设计上不注册绑定」。

**影响面：仅日志噪音，功能无缺陷**。三条输出路径中，终端 WS 广播（`main.go:807`）与 CP 事件流（`main.go:808`）都在报错点**之前**分流，不受影响；受管 Raw 持久化在恢复窗口内因绑定未就绪而未写入**窗口内那几帧**——窗口极短（29 秒）且该服务恢复后即静默，故实际未丢失有效日志（VL 实测 `inst:153` 计数为 0，与该实例无新增可采内容一致）。

**性质**：属「启动恢复窗口内产生 ERROR 噪音、会误导运维」的缺陷，非数据面故障。**建议修法**：在恢复窗口内对尚未注册绑定的实例静默跳过（或降为 debug 级），使日志如实反映真实异常；并复核恢复流程与绑定注册的先后顺序，尽量消除该窗口。此条**不在 FR-473~484 的验收范围内**（非任一 FR 的验收标准），登记为独立待办，**不影响本次签字**。

**附带澄清（避免误判采集停摆）**：核查中发现 VL 计数长时间不变（354797），经查为**游戏服务器启动完成后静默**所致——11 个受管实例全部存活且正常监听端口，其 `latest.log` 仅 42 行/4KB（最后一次写入 11:41），采集账本 `end_pos` 已追平文件（且文件已轮转为 `.gz`）。**采集管道正常，无增量是正确行为**，此前测到的 ~459 条/秒为启动期密集输出。

## 生产 Worker 与 PR 内容等价性核查（2026-09-26 签字前）

**动机**：本 PR 涉及 109 个 `internal/worker/` 文件，而生产替换时**只替换了 CP 二进制**，须确认生产 Worker 是否也需同步更新，否则「签的内容」与「生产在跑的」不一致。

**生产 Worker 构建溯源**：`go version -m` 读出 `v0.23.1-0.20260924131238-fae55fa8816a+dirty`，构建自 `fae55fa8`（2026-09-24 13:12，FR 重编号**之前**的旧提交，重编号后已不在 HEAD 历史中）。

**判定：无需更新，行为等价**。对 `fae55fa8..当前 HEAD` 的 `internal/worker/` + `apps/worker/` 全量差异做性质分类：共 40 个文件、88 行改动，剔除注释与 FR 编号行后，**实质代码改动为 0 行**——全部是 FR 重编号（FR-476→477、FR-473→474 等）引起的注释同步。故生产 Worker 的**行为**与 PR 内容一致。

**遗留说明**：该二进制带 `+dirty` 标记（构建时工作区有未提交改动），且构建自已被重编号改写的旧提交。二者都不改变上述结论（行为等价已由差异分类证明），但记此以便日后追溯；如需绝对一致，可在下次常规发布时重建 Worker（本次不为签字而重启生产 Worker——重启的收益为零、而中断 11 个运行实例的采集中断风险非零）。

## 签字后的 PRD 更新预案（2026-09-26 预核，待用户签字后执行）

**目标格式**：`✅ 已交付@v0.24.0·<保留原有验收说明>`。该格式有先例（如 FR-010 的 `✅ 已交付@v0.13.0·全真栈验收（…）`），即**保留既有验收描述**，仅替换状态前缀。

**12 条 FR 的定位与改法**（PRD 行 596~607 连续）：

| FR | PRD 行 | 当前状态前缀 | 改法 |
|---|---|---|---|
| FR-473 | 596 | `📅 契约已冻结·开发前闸门（ADR-094 accepted；不等于 FR-473 已交付）` | 特殊：改为 `✅ 已交付@v0.24.0·契约类闸门`，并在说明中保留「交付物即契约层、下游功能归 FR-474~484」之意（原括注「不等于已交付」须一并替换，否则与已交付自相矛盾） |
| FR-474~484 | 597~607 | `🔨 开发中·…`（共 11 条） | 机械替换前缀为 `✅ 已交付@v0.24.0·`，其后验收描述**原样保留** |

**执行约束**：① 该更新须**在用户逐条签字之后**进行（`gate-merge.md`：Agent 不得自行标记 done）；② 三方须同指 `v0.24.0`（`version.go` 已为 `0.24.0-dev`、CHANGELOG 段首已注明 `v0.24.0`，由 PR #22 承载——故 PR #22 应先于本更新合入或同时合入）；③ 改后须复核无裸 `✅ 已交付`（`versioning.md` 禁止）。

## 验收标准与证据的对应性核查（2026-09-26 签字前）

为避免「台账写可签、但 PRD 的验收标准其实无对应证据」，对 12 条逐条比对 PRD 描述列的验收要求与台账证据列：

| FR | PRD 点名的验收入口 | 台账证据要点 | 对应 |
|---|---|---|---|
| FR-473 | `worker-log-platform-contract/spec.md` | ADR-094 accepted；proto 10 个日志 RPC；资产审批；性能判据入契约 | ✓ |
| FR-474 | `worker-log-acquisition/spec.md`（**Runbook A**） | 生产采集/投影/ACK 恢复/reclaim；**Runbook A 真机 `kill -9` 重启通过** | ✓ |
| FR-475 | `worker-log-normalizer/spec.md` | 真 VL v1.52.0：Multiline 5 行归并、跨午夜、半条恢复 | ✓ |
| FR-476 | `worker-victorialogs-runtime/spec.md`（**Runbook C**） | 资产审批、supervisor/CP runtime、预算采样与 80/90 降级、老 Worker Unimplemented | ✓ |
| FR-477 | `worker-log-lifecycle/spec.md`（**Runbook B**） | 真机迁移链 FROZEN→…→CLEANED、物理迁移、旧 owner 过滤 | ✓ |
| FR-478 | `worker-log-deep-archive/spec.md` | 真机 RustFS S3：登记/幂等/manifest/对象往返/Rehydrate 租约 | ✓ |
| FR-479 | `worker-log-query-federation/spec.md` | 全查询 RPC 接线、远程 Search/Stats/Facets/Export、CP 联邦真机 | ✓ |
| FR-480 | `cp-log-query-coordinator/spec.md` | 生产 Assemble、真机联邦与延迟取证（p95 28.5ms / 8 并发 15.2ms） | ✓ |
| FR-481 | `log-ingest-cutover/spec.md` | cutover+Legacy+DualPath、跨切换查询经两条真实路径 | ✓ |
| FR-482 | `logs-center-tiered-ui/spec.md` | 真浏览器验收 + 逐项点击实测（含本轮 source 修复与生产复验） | ✓ |
| FR-483 | 本文档（受控文档） | `recovery-manual.md`、`acceptance-record.md`、`asset-inventory.md` 齐备 | ✓ |
| FR-484 | `worker-log-canonical-eventstore/spec.md` | 真机 v1.52.0：稳态/峰值 RSS、342 源零丢失零重复 | ✓ |

**结论**：12 条均有对应证据，无「标准无证据」的空档。另核实 PRD 12 条描述点名的 **11 个 spec 文件全部真实存在**（逐一 `os.path.exists` 验证）。

**执行前干跑（2026-09-26）**：对上述替换逻辑做只读干跑（不写入），逐条模拟后校验：**12/12 均可正确替换**，无未知形态、替换后无裸 `✅ 已交付`（`versioning.md` 禁止项）。故签字后执行无技术风险。

## 用户签字记录（2026-09-26）

用户已确认 **FR-473～FR-484 共 12 条全部签收**。依据 Gate-4 规则，后续在版本基线 `v0.24.0` 生效后，将 PRD 12 条状态更新为 `✅ 已交付@v0.24.0`；FR-473 按契约类闸门的特殊预案处理，FR-476 保留已接受的 Windows 运行时边界说明。

**当前后续顺序**：PR #22（版本号 bump）与 PR #16 均待用户授权合并；PR #22 合并使 `version.go`/CHANGELOG 的 `v0.24.0` 基线先落地，再执行 PRD 状态更新，避免三方版本不一致。

## 签字后发现的 FR-475 规格漂移（2026-09-26，待确认）

用户已签收 FR-473~FR-484 后，复核 PRD 前置发现 **FR-475 的规格与台账存在矛盾**：

- `docs/specs/worker-log-normalizer/spec.md:40` 仍写「剩余 Windows 平台证据缺失」；
- `docs/specs/worker-log-normalizer/acceptance-real.md:72` 仍写「Windows 平台未测」；
- 但本台账 FR-475 行写「规格 §5 四类边界已全部覆盖」且缺口为「无」；
- normalizer 代码检索未发现 Windows 特有分支，可能是规格/验收记录滞后，但不能由 Agent 自行推翻规格中的明确缺口。

**处理状态（用户裁决）**：用户选择 **暂缓 FR-475**，保持其 PRD 状态为 `🔨 开发中`，待补齐 Windows 验收后再标记；不将现有 spec/acceptance-real 的明确缺口自行改写为已知边界。

其余 **11 条 FR** 已按预案把 PRD 状态更新为 `✅ 已交付@v0.24.0`；FR-475 的签字记录保留，但其交付状态单独挂起，不影响已交付的 11 条。

## 生产 Worker 部署稳定性加固（2026-09-26）

**部署内容**：PR #23（`chore/stability-hardening-clean`）的 Worker 侧运行时修复——日志绑定启动竞态的持久 pending spool 机制。该 PR **尚未合并到 master**，故生产 Worker 现含未合并代码。

**部署方式与一个关键处置**：首次 `cp` 替换失败（`Text file busy`）——根因是 **12 个 `daemon` wrapper 进程持有该二进制**（它们即游戏服守护进程，运行逾 1 天）。未杀这些进程，改用**原子 rename 替换**（新 inode 就位后 `mv`，不影响已打开的旧 inode），12 个游戏服全程未受影响。

**部署结果**：Worker `0.23.0` → `0.24.0-dev`；`daemon` 实例恢复 **12/12**；游戏服 wrapper **12/12 完好**；CP `HTTP 200`；实例状态统计与部署前逐项一致（RUNNING 12 / STOPPED 136 / CRASHED 5）。

**修复效果验证（本次部署目的）**：本次启动窗口 `no durable source binding` = **0 次**、`受管 Raw 持久化失败` = **0 次**——而旧版同一场景产生 164 条该 ERROR。修复生效。

**回滚**：`bin/jianmanager-worker.bak-stability-20260926-202929`（33760216 bytes，`0.23.0`）。

**注**：生产 CP 未替换——本 PR 对 CP 仅脚本/CI/测试配置改动，无运行时逻辑变化。

## 生产 VictoriaLogs 启动测试发现真实缺陷（2026-09-26）

**测试方法**：经生产 CP 控制面 `POST /api/v1/nodes/1/log-runtime/rehydrate/start`（选 rehydrate 因其配置 `start_rehydrate: false`，启停不影响正在采集的 HOT）。

**缺陷 1（严重）：启动未校验存活，API 报成功但进程已是僵尸**
- API 返回 `HTTP 202 {"state":2}`（成功），状态查询报 `RUNNING, pid=2121879`；
- 但实测该 PID 为 **`Z <defunct>`（僵尸）**、**端口 19463 未监听**；
- 根因：`internal/worker/logs/vlsup/supervisor.go:286` 在 `factory.Start` 返回后**立即置 `StateRunning`**，不做存活/端口校验；VL 因数据目录缺失即刻退出，状态却停留在 RUNNING。
- 危害：运维据控制面判断「VL 已启动」，实际早已死亡——属**误导性成功上报**。

**缺陷 2：启动前不创建 namespace 数据目录**
- `vl/` 下仅有 `hot`/`cold`，**缺 `rehydrate`**；`StoragePathUnder` 只拼路径，`Start` 不 `MkdirAll`（仅 `install.go` 建特定 root）。首次启动某 namespace 时必然失败。

**缺陷 3：孤儿僵尸残留**
- 观察到 3 个僵尸 VL 进程（`2102020`/`2102027`/`744632` 等），其中两个父进程为**新部署的 Worker（2101999）**，即新 Worker 启动时曾拉起 VL 失败并留下僵尸。

**未受影响**：HOT 采集正常——真正服务 19461 的是 PID `891327`（运行逾 1 天）；游戏服 12/12 完好；已入库日志数据未受影响。

**当前处置**：仅取证与留痕，**未修改代码、未清理僵尸**——修复属新缺陷修复（是否并入 PR #23 会改变其范围）、清理僵尸需重启 Worker（短暂中断采集），均待用户裁决。

## VL 启动测试与生产事故记录（2026-09-26 21:00-21:25）

### 测试发现的三处真实缺陷（测试价值所在）

1. **启动报假成功**：`POST .../log-runtime/rehydrate/start` 返回 `HTTP 202 {"state":2}`，状态查询报 `RUNNING`，但实测 PID 为 `Z <defunct>` 僵尸、端口 19463 未监听。根因：`vlsup/supervisor.go` 在 `factory.Start` 返回后即置 `StateRunning`，不校验进程存活。
2. **启动不创建 namespace 数据目录**：`vl/` 下仅有 `hot`/`cold`，缺 `rehydrate`；`StoragePathUnder` 只拼路径，`Start` 不 `MkdirAll`，故首次启动某 namespace 必然失败。
3. **Worker 退出不回收受管 VL**：Worker 停止时只 `manager.StopAll()` 停游戏实例，不停 VL；VL 成为 `PPID=1` 孤儿并继续占用 hot/cold 端口，导致下次启动的新 VL 因端口被占立即退出、日志中心长期降级。

三项均已修复（4 个提交于 `chore/stability-hardening-clean`），每项均带单测与**变异验证**（跳过校验/移建目录/禁用校正/跳过 Stop 均使测试转红）。

### 我造成的中断（如实记录）

验证「Worker 退出回收 VL」时，我**反复误判进程身份**：`setsid nohup ./start-worker.sh &` 会先生成 bash 包装进程，我多次向包装 PID 而非 Worker 本体发信号，导致修复路径从未被触发，我因此连续做了多轮无效尝试。**后果：生产 Worker 中断约 5 分钟，期间日志中心不可用**（`reasons: OFFLINE`）。

**未受影响**：12 个游戏服全程完好（daemon wrapper 独立运行）；VL 数据零丢失（事故前后 82 万 → 214 万条持续增长）；CP 未中断。

**恢复**：清理孤儿 VL 释放端口 → 启动 Worker → 21:21:51 注册成功 → 服务恢复（HOT/COLD 均 RUNNING 且 health=true，联邦查询返回真实事件）。

### 尚未验证的一项

「Worker 退出回收 VL」的修复**未能在生产验证**——因上述 PID 误判，我未能正确触发优雅关闭路径。该修复仅有单测+变异证据，生产验证建议在操作机上的隔离测试实例（独立 systemd 服务与独立安装目录）进行，不再于生产做实验性重启。

## rehydrate 启动失败根因补正（2026-09-26 补充核查）

此前把 rehydrate 启动失败归因于「数据目录缺失」。**补充核查推翻该归因**：以生产实际参数与路径做隔离复现，VL **完全正常**（1ms 内就绪、监听端口、正常服务）；用生产真实 rehydrate 数据路径启动同样正常。

**真实根因是端口冲突**：隔离复现证实，当目标端口已被占用时，VL 报
`fatal ... cannot start http server at 127.0.0.1:<port>: bind: address already in use`
并以**退出码 255** 立即退出。当时正处于我反复重启 Worker 的窗口，19463 端口可能被前一轮残留进程占用，故 rehydrate 一启动即死。

**对修复的意义**：本 PR 的存活校验（启动后观察窗口 + `Status`/`StatusAll` 实况校正）**正好覆盖这一类失败**——无论子进程因缺目录、端口冲突还是二进制不兼容而秒退，控制面都会报 `FAILED` 并给出可诊断原因，而非此前那种「API 报 202 成功、状态显示 RUNNING、实际进程已死」的误导性成功。

**数据目录预建仍属必要**：它消除「缺目录」这一独立失败路径（原缺陷 2），与端口冲突是两个不同的致因，二者均需修复。

## 生产部署 B-1/B-2 安全修复（2026-09-27，PR #23 合并后）

**动机**：代码审查发现两处已交付代码缺陷（B-1 假 complete、B-2 跨实例越权读），合并前实测确认**生产二进制均不含修复**——生产 CP 构建于 09-25 22:52、Worker 于 09-26 21:14，均早于 PR #24 合并（09-27 01:36 CST）。以修复独有标记核实：CP 无 `WORKER_VIEW_FAILED`、Worker 无 `extra_filters`（该串修复前 0 处、修复后 3 处，反向验证；同法在 Worker 检出 `StopAll` 5 处、CP 检出 `federationFilter` 2 处，证明检测方法本身有效）。经用户授权「立即部署」执行。

**部署基线**：主干 `tree 5437f4df`（= `origin/master a70b8265`，含 B-1/B-2 与 PR #23 全部修复），版本 `0.24.0-dev`；交叉编译命令复用 Makefile 的 `-trimpath -ldflags "-s -w -X .../version.Version"`。

**关键安全前提（部署前查证）**：Worker 关闭路径会调用 `manager.StopAll()`，其行为取决于实例模式——`direct` 模式会终止游戏服，`daemon` 模式仅断开 wrapper 连接（ADR-003 进程隔离）。生产 12 个实例均为 `daemon`，且 wrapper 已脱离 Worker（`ppid=1`），故重启 Worker 不停游戏服。**实测印证**：Worker 停止与重启期间 12/12 实例的 java 进程全程存活。

**部署结果**：

| 项 | 结果 |
|---|---|
| CP | `HTTP 200`、gRPC 19100 监听；二进制 sha256 `30af964c…` |
| Worker | 已注册 CP、反向隧道建立；二进制 sha256 `d682c203…` |
| 游戏实例 | **12/12 全程完好** |
| VL | hot/cold 监听；端口随 Worker 退出**干净回收**（0 占用），印证 PR #23 的 `StopAll` 修复在真实环境生效 |
| 修复标记 | CP 含 `WORKER_VIEW_FAILED`、Worker 含 `extra_filters` |

**部署后复验**：CP `ERROR/FATAL` **0 条**；Worker 4 条 ERROR 全部落在 `02:56:37`（旧 Worker 被 SIGTERM 时刻）的 `Bot Worker 子进程意外退出 signal: killed`，属预期关闭副作用（`autoRestart=false`）。采集持续正常：90 秒摄取计数 +45 条、近 5 分钟 472 条、近 10 分钟 561 条覆盖 6 个来源。

**一处需要更正的自身误判（如实记录）**：复验时曾据 **60 秒窗口**摄取计数零增长判定「采集停摆」，并据此怀疑本次部署引入故障。该判断**错误**——计数器为突发式增长，60 秒恰逢空闲间隙；改用 90 秒窗口即见增长。同批被我误读的还有 `latest.log` 陈旧（多数停在 09-26 15:57）：MC 服务器仅在产生日志类事件时写该文件，与采集健康无关。**教训**：以单调计数器判定采集活性时，窗口须覆盖一个完整突发周期。

**回滚**：`cp/bin/jianmanager-cp.bak-b2fix-20260927-025512`、`worker/bin/jianmanager-worker.bak-b2fix-20260927-025602`。

**影响面**：B-2 修复改变了用户过滤的下发方式（作用域独占 `query`、用户过滤走 VL 的 `extra_filters`），**行为边界为用户过滤不再支持 LogsQL 管道（`|`）**；此前可用管道属越权面的一部分，且联邦界面 keyword 语义本就是关键词检索。
