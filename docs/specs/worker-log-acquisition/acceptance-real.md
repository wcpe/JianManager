# FR-473 磁盘容量验收记录（受控）

日期：2026-09-24
主机：node-main（生产主机，IP 已脱敏）
脚本：`.tmp/fr434-acceptance/main.go`（不入库）
数据根：`/tmp`（真实文件系统，采样不伪造）

## 1. 本轮与此前状态的区别

此前该条为「**磁盘满真实填充按用户要求排除**」，仅有纯逻辑单测（`TestEvaluateCapacityDiskThresholds`
直接构造 `DiskUsagePercent=80/90`）。本轮补上**真机**证据：用**真实采样**驱动**真实阈值比较**，
并让 PAUSED 经**真实 `Pipeline.Ingest`** 落到账本。

## 2. 真实填盘验收（容器内受限文件系统，已完成）

用户确认本机可用 Docker（`docker` 组已含操作用户，需 `sg docker` 激活本会话）后，
用**受限 tmpfs 容器**做真实填盘：`docker run --rm --tmpfs /data:size=48m` + 静态二进制。

**实测输出**：

```
初始使用率: 0.00%
  [80%] 真实使用率 83.33% → DEGRADED_STORAGE（disk usage 83.3% >= 80.0%; degraded）
  [90%] 真实使用率 91.67% → PAUSED（disk usage 91.7% >= 90.0%; pause irreversible writes）必记缺口=true
填盘结束：使用率 100.00%，写入 48 MiB

[3] 暂停落账本（真实 Pipeline.Ingest）
    ✓ Ingest 被拦下：acquire: capacity paused: disk usage 100.0% >= 90.0%; ...
    ✓ 磁盘满时未投递
    ✓ 已置 AcquirePaused
    ✓ 已记 1 个未解决缺口

[4] 真实 ENOSPC
    ✓ 写满时底层返回错误（可观测）：write /data/fill.bin: no space left on device
```

| 验收项 | 观测 | 判定 |
|---|---|---|
| 真实采样驱动判定 | 83.33% → `DEGRADED_STORAGE`；91.67% → `PAUSED`（必记缺口） | ✅ |
| 暂停落账本 | `Ingest` 被拦、未投递、`AcquirePaused` 置位、记 1 个未解决缺口 | ✅ |
| **真实 ENOSPC** | 填至 100%，`write … no space left on device` 可观测 | ✅ |

隔离性：容器为 `--rm` 一次性，填的是容器内 48MiB tmpfs，**未触碰**任何宿主机挂载点；
运行后确认无残留容器。

## 3. 此前受阻记录（保留，说明为何先用了替代方案）

本轮先尝试在隔离文件系统内真实填盘，逐一受阻，记录如下以免被误读为偷工：

| 方案 | 结果 |
|---|---|
| Docker 容器（受限卷） | **不可用**：当前用户不在 `docker` 组（`/var/run/docker.sock` 属 `root:docker`） |
| `unshare --user --mount` 自建 mount ns | **不可用**：`Operation not permitted`（内核/容器策略禁用） |
| 无 root `mount -t tmpfs -o size=64M` | **不可用**：`must be superuser to use mount` |
| 填 `/tmp` 到 90% | **不采用**：`/tmp/e2e3/mock_beacon.py` 是他人的**长期服务**（已运行 3 天 7 小时，PID 3340281），填盘会危及它 |
| 填 `/home` 到 90% | **不采用**：`/home`（1.4T）是生产数据盘，正是用户当初要求排除的对象 |
| 其余挂载点（`/`、`/usr`、`/var`） | **不采用**：系统盘，影响同机其他服务 |

→ 结论：本机**不存在既隔离又可填的挂载点**。因此改为下方方案。

## 4. 采用的补充验收方式（真实采样驱动，不伪造指标）

容量阈值本就是**可配项**（`log_capacity.degraded_at_percent` / `pause_at_percent`，
校验规则 `0 < degraded < pause <= 100`，见 `internal/worker/config.go:301`）。
因此用**真实 `DiskCapacityProvider`（真实采样当前文件系统）+ 真实阈值比较**，
把阈值设到真实使用率附近来触发各档判定——全程走生产代码路径。

实测输出（真实使用率 62.60%）：

**[1] 采样真实性**

```
df 直读          = 62.60%
CapacityProvider = 62.60%   → 与 df 一致 ✓
```

**[2] 三档判定（真实使用率 + 真实阈值）**

| 场景 | degraded | pause | 判定 |
|---|---|---|---|
| 宽裕盘 | 72.60 | 82.60 | `OK` ✅ |
| 使用率刚过降级线 | 62.59 | 72.60 | `DEGRADED_STORAGE` ✅ |
| 使用率刚过暂停线 | 52.60 | 62.59 | `PAUSED` ✅ |

**[3] 暂停落账本（经真实 `Pipeline.Ingest`）**

```
✓ Ingest 被容量门禁拦下：acquire: capacity paused: disk usage 62.6% >= 62.6%; pause irreversible writes
✓ 暂停状态下未投递（deliver 钩子未被调用）
✓ 已置 AcquirePaused
✓ 已记录 1 个未解决缺口（无静默丢弃）
```

规格 §5「容量满暂停并报告缺口」→ **经真实链路验证：暂停生效、必记缺口、无静默丢弃**。

## 5. 未覆盖边界

- ~~未做真实填盘~~ —— **已由容器内真实填盘补上**（含 ENOSPC），见第 2 节。
- **未测 80/90 以外的自定义阈值组合**（仅验证三档边界语义）。
- 未在受管 VL 进程参与下测（本项属采集面准入，与 VL 无关）。

## 6. 判定

规格 §5「容量满暂停并报告缺口」→ **真实填盘验收通过**（含此前未覆盖的真实 ENOSPC 边界）。
本项无遗留缺口。
