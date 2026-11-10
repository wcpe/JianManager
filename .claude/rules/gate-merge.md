# Gate 4: 合并/发版门禁

> 代码合并到 master 前必须满足以下条件。

## Checklist

### 一致性
- [ ] 实现和 `docs/specs/<feature>/api.md` 一致
- [ ] 实现和 `docs/ARCHITECTURE.md` 架构一致（未引入未记录的模式）
- [ ] 数据库 migration 和 ARCHITECTURE.md 中的表结构一致
- [ ] **FR 验收标准逐条通过，且由用户确认（Agent 不得自行标记 done）**

### 文档同步
- [ ] `docs/ARCHITECTURE.md` 已更新（如有架构变更）
- [ ] `docs/API.md` 已更新（如有 API 变更）
- [ ] `docs/adr/` 已追加（如有架构决策变更）
- [ ] `docs/PRD.md` 中对应 FR 状态已更新；已交付必须写成 `✅ 已交付@vX.Y.Z`，不得留下裸 `✅ 已交付`

### 代码质量
- [ ] 无编译错误
- [ ] 无 lint 错误
- [ ] 新增 / 修改的代码里没有 FR / ADR 编号：`sh .claude/scripts/check-no-ref-in-code.sh` 通过（存量豁免见 [no-ref-in-code.md](no-ref-in-code.md)）
- [ ] 新增 / 修改的**文档**里没有 git commit hash：`sh .claude/scripts/check-no-hash-in-docs.sh` 通过（squash merge 后 hash 必悬空，见 [no-hash-in-docs.md](no-hash-in-docs.md)）
- [ ] 核心逻辑有测试覆盖
- [ ] 状态 / 迁移类变更已按 [testing-and-quality.md](testing-and-quality.md)「状态 / 迁移类变更」跑通「迁移 → 首启 → 停 → 二启 → 三启」（含脏环境），并附逐轮记录
- [ ] 长临界区审计：按本文「长临界区审计」清单逐条核对（无共享锁跨越 Poll / 投递 / 持久化，锁获取点标注锁序与最坏持有量，跨进程调用不在持锁区内）

### 发版时额外检查
- [ ] `CHANGELOG.md` 已更新
- [ ] 版本号已确定
- [ ] 合并经 PR 完成，未直推主干；发版提交同样是独立 PR（chore(release)）
- [ ] **基线提交的远程 CI 已全绿**（见下节红线，未绿则禁止发版）
- [ ] 部署含状态 / 迁移类变更时，已按本文「部署后复验」在部署后**立刻再重启一次**并验证通过

## 发版红线：CI 未绿禁止创建发版 commit 与 tag

> 发版 commit（`chore(release): 发布 X.Y.Z`）与 tag `vX.Y.Z` 是对「该提交已通过全部质量门禁」的承诺。CI 未绿就创建它们，等于把未经门禁验证的提交标记为可发布。

**硬性要求**：创建任何发版 commit 与 tag **之前**，必须先确认**基线提交**（发版 commit 的父提交，即当前主干 tip）在远程 CI 上**全绿**。

- **唯一判定依据是远程 CI 结论，不得用本地测试结果替代。** 本地 `go test` / `vitest` 全绿**不能**代替 CI：开发机负载与环境差异会让两者不等价（实例：本机 `load average ≈ 30` 时前端 vitest 因 10s `testTimeout` 批量假失败，而**同一提交**在 CI 上 2349 用例全绿）。
  - **2026-10-27 补充：`go test` 同样会因宿主机载批量假失败，不只是前端的 `testTimeout`。** 当 `load average` 达 100–190（本机 40 核上常驻 104 个 java、多个 worker 与生产 CP/Worker）时，以下两类失败与代码无关：
    - **包级超时**：`panic: test timed out after 10m0s` + `running tests: <某用例>`。这不是用例失败，是整包在负载下跑不完 10m 默认时限。仓库已有 `scripts/go-test.sh` / `go-test-race.sh` 的分组与超时机制，可用 `CORE_TIMEOUT` / `RACE_TIMEOUT` 放宽。
    - **负载敏感的性能断言**：实测至少 4 条（`TestSortEventsHandlesLargeReversedInput` 500ms 线实测 505ms、`TestSingleCommitLatencyBoundedAtScale` 50ms 线实测 66ms、`TestLoadSamplingThroughRealManagerWALAndDelivery` 报「未投递全部源」、`TestWiringRecoveryLoopRetriesWithRetentionDisabled` 报「条件恢复后没有续跑」）——**前两条是耗时阈值，后两条是 `Eventually` 类等待条件**，同一个逻辑在各次运行间的差别可达数十倍。
  - **区分方法（必须做，不能默认是环境噪声）**：先 `cat /proc/loadavg` 记下负载，再对失败用例**单跑 3 次**（`go test ./pkg/ -run '^TestX$' -count=3`）。若单跑全绿且耗时远低于阈值，才能判为负载噪声；否则必须按真缺陷排查。**`WARNING: DATA RACE` 计数为 0 是「没有数据竞争」的直接证据**，与用例断言失败是两回事，不得混为一谈。
  - 本条不构成放宽门禁的理由：真缺陷在单跑时同样会红；而负载噪声下的红不得被当成「已修复」也不得被当成「代码问题」。两方向都必须拿单跑证据说话。
- **CI 红即禁止发版，flake 变红同样禁止。** flake 不是豁免理由——它使门禁本身不可信，必须先使其变绿（修 spec 稳定性，或重跑并确认结论为 success）再发版。
- 核对命令（要求 `conclusion == "success"` 且 `headSha` 等于待发版的基线提交）：
  ```sh
  gh run list --workflow=ci.yml --limit 1 --json conclusion,headSha,displayTitle
  ```
- **禁止「先打好 commit/tag，等 CI 绿了再推」。** commit 与 tag 一旦创建，本地就已形成「已发布」事实，极易随 `push --follow-tags` 意外外泄。CI 未绿时，`version.go`、CHANGELOG 归档段、tag 一律不得改动。
- CI 红时的正确顺序：定位原因 → 修复（经 PR 合入主干）→ 等主干 CI 全绿 → 再执行发版流程。
- 已在 CI 未绿状态下误建的发版提交与 tag，应**立即撤销**（删除本地 tag + 回退发版提交），待 CI 转绿后重新执行。

## 部署后复验：状态 / 迁移类变更必须再重启一次

> 部署内容含**状态 / 迁移类变更**（索引 / 账本 / 投影代次 / schema / 回滚脚本 / 换存储）时，**部署后立刻再做一次重启并验证**，通过才算部署完成。「首启通过」是伪完成——首启常落在冷路径 / 低号段上侥幸过关，问题往往只在第二次启动才暴露。

**为什么（事故实证）**：2026-10-01 FR-496 生产部署，**04:41 首启通过**，**04:50 二启后卡死**，直到 05:19 回滚才恢复——**卡死约 27 分钟无人察觉**：同期日志只是「同 (代次, 源, 窗口) 反复重查、每次卡满 5 分钟校验窗口」的静默重试，没有「启动失败」这类显式返回，也没有告警。若部署流程在首启后就宣告完成，这次事故只能等用户报障才发现。

**判据**：
- 状态 / 迁移类变更的部署，**收尾步骤固定为**：重启一次 → 验证通过 → 才算完成；验证内容与「二启」口径同 [testing-and-quality.md](testing-and-quality.md)「状态 / 迁移类变更」（启动结果 + 代次 / 游标 / 账本 / 索引行数等关键字段，不是只看进程活着、端口通）；
- 二启验证**当场做**，不得留到「下次重启时顺便看看」；
- 首启通过**不得**作为「部署完成」的结论上报；
- 出现「启动慢 / 启动卡住无告警」时，按二启口径自查，不要默认是环境噪声。

## 长临界区审计（代码评审必查）

> 2026-10-01 生产事故（FR-499）：实例登记路径抢 `cycleMu`，而该锁被**整轮采集**独占（`pollOnce` 逐源 `Poll` → `persist` → VL 投递 → 投影校验；ticker 250ms 使两轮之间几乎无空档，饱和期近乎连续持有，实测单轮 3.15s、生产可达分钟级）→ CP 的 `CreateInstance`（「登记实例日志采集」这一步）**四次全 DeadlineExceeded**，**11 台实例无法启动**。教训是：**一个与该功能无关的调用方，会被另一条长流程的锁拖死**——锁纪律必须进评审，不能靠事后排障。行级锁审计表见 `docs/specs/log-ingest-register-lock/spec.md` §2。

评审任何涉及并发 / 共享状态的改动时，逐条核对：

- [ ] **长流程不得持共享锁跨越慢操作**：`Poll` / 投递（VL 写）/ 持久化（索引、DB 事务）/ `Flush` / `Close` / 网络往返，一律不得落在共享锁的临界区内；
- [ ] **锁获取点必须标注锁序与最坏持有量**：新增或修改锁（含 `mu`、`cycleMu` 这类进程内互斥）时，写清它在**锁序**中的位置（如 `cycleMu → registerMu → pendingMu → mu`）与**饱和场景下的最坏持有时长**，并给出量化口径——持续有排队者的锁**不得**用 `TryLock` 判定持有（Go `sync.Mutex` 饥饿模式下 `mutexStarving` 置位，锁空闲的瞬间也返回 false，会读出「从未释放」的假象）；
- [ ] **跨进程调用不得在持锁区内**：gRPC / HTTP / 子进程 IO 不得在持锁状态下发起；
- [ ] **短路径与长流程分锁**：控制面入口、运维动作等有固定 RPC 截止时间的短路径，不得靠「等长流程跑完」来满足响应预算；把它们从长流程的锁上摘出来，而不是把等待面转嫁给调用方。

## 检查方式

按此 checklist 逐项核对。

### race 门禁在本机的读法（2026-10-27 实测）

`scripts/go-test-race.sh core` 跑全量时，本机（40 核 / load 100–190）的实测读数：

| 次 | 时限 | 结果 |
|---|---|---|
| r1 | 10m | 59 ok，3 个包级超时 |
| r2 | 20m | **60 ok / 67 包**，1 段 DATA RACE，2 个包级超时 |

**2 个包级超时**是 `internal/controlplane/router` 之外的 `internal/controlplane/service` 与 `internal/worker/logs/ingest`——1200 秒都跑不完，属规模问题（既有 837 处 `setupTestDB` × 115 模型 AutoMigrate 的老账），要单独给更长时限。

**1 段 DATA RACE 是真竞争**，在测试夹具里（`retention.fakeMover` 被 `Driver.Run` 的 goroutine 写、主 goroutine 轮询读）。已修，并同批修掉两条「-race 下必然红」的绝对耗时/预算断言——修法是**只放宽耗时与预算，不放宽断言**（见 `query/race_on_test.go`、`vlsup/race_on_test.go`）。

**定向复跑才是判断依据**：对 6 个重点包用 `TMPDIR=/tmp go test -race` 跑，DATA RACE = 0、5 包 ok。全量跑慢到超时的包不等于有竞争——但也不能因此跳过它们。

**跑 go test 一律加 `TMPDIR=/tmp`**：若本机默认 `TMPDIR` 指向一个长目录（如 `$HOME/tmpwork`），会让 wrapper 类用例的 unix socket 路径超到 111 字节、超过内核 `sockaddr_un` 的 107 上限，表现为「20 秒后某个产物仍未出现」这种离根因很远的报错。

**2026-10-27 最终验证**（当时 HEAD，`TMPDIR=/tmp`）：对四处修复涉及的 4 个包跑
`go test -race` —— `retention` / `query` / `vlsup` / `process` **全 ok，`DATA RACE` 计数 = 0**。
这是「修完之后确实没有竞争」的直接证据，与「全量跑某些包超时」是两回事：
超时只说明跑不完，不说明有竞争；而定向跑干净也不等于全量无竞争，两个证据都要。
