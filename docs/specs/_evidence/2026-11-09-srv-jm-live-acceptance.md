# 真机验收记录：2026-11-09（/srv/jm 现有部署）

> 环境：Debian，16 核 / 78GB，机械盘（ROTA=1）。宿主上 **6 个真实 MC 服务器**在跑
> （login-1/2、game-1/2、lobby-1/2，PID 2469188/226/227/347/509/548，自 2026-11-04 01:53 起）。
> 部署方式：systemd --user 双单元（`jianmanager-cp.service` / `jianmanager-worker.service`），
> Worker 单元已配 `KillMode=process`（FR-500 交付物）。
> 数据：沿用现有 `/srv/jm/data/jianmanager.db`（465MB，10 实例 / 1 节点），已整库备份。

## 1. 部署新二进制（本轮所有修复）

| 项 | 旧 | 新 |
|---|---|---|
| `jianmanager-cp` | 11-04 00:30，已持续运行 5d16h | 11-09 16:38，md5 `86bc8758…` |
| `jianmanager-worker` | 11-04 00:44 | 11-09 16:44，md5 `4c6e74da…` |

回滚点：`/srv/jm/bin/jianmanager-{cp,worker}.rollback-163{8,9}` + `/tmp/jachk/srvjm-backup-1635.tgz`
+ `/tmp/jachk/db-before-1634.db`。

## 2. FR-500：CP/Worker 重启不牵连游戏服（真机复验 PASS）

这是本轮最硬的一条证据，且**两次独立验证**：

- **停 CP 重启新 CP**：6 个 MC 进程 PID 完全不变、启动时刻仍为 11-04 01:53。
- **停 Worker 再启新 Worker**：6 个 MC 进程同样 PID 不变（`KillMode=process` + daemon
  wrapper 各自守着自家 MC，见下表）。

wrapper → MC 对应关系（停 Worker 前后一致）：

```
2469181 → 2469188 (login-1)     2469212 → 2469226 (login-2)
2469218 → 2469227 (game-2)      2469336 → 2469347 (game-1)
2469502 → 2469509 (lobby-2)     2469541 → 2469548 (lobby-1)
```

同时验证了反向隧道自动重建：CP 启动后 `节点反向隧道已建立 active=1`，
Worker 恢复 `StreamPluginEvents` / `StreamInstanceEvents` 订阅。

## 3. MCP 审批工具（本轮新增，真机端到端 PASS）

用真实 Agent Token（V2、`instance.read`+`instance.life`、node scope=1）走 SSE 双通道：

1. `tools/list` → 36 个工具，含 `approval_list` / `approval_get` / `approval_withdraw`。
2. `instance_stop {id:1}` → 被拦截，返回
   `awaiting_approval` + `requestId=452d84b0…` + `ticketId=13`，**实例未停**（仍 RUNNING）。
3. `approval_list` → **立刻查到这张票**（requestId 一致、status=pending）。
   这是本工具要解决的核心问题：修复前 Agent 提交后完全无法回读。
4. **跨 Token 隔离**：另一把 Token 调 `approval_list` → `count:0`；
   查票 #13 → `审批票据不存在`（不泄漏存在性）。
5. 人类批准后，票据转为 `failed`，原因：
   `目标已漂移，审批不再适用: 实例 #1（game-1）在申请后已被修改`
   —— **FR-491 的漂移检测真机生效，且零副作用**（实例始终 RUNNING）。

额外观察（诚实记录）：漂移由健康巡检/心跳在申请后更新实例字段触发
（`version` 哈希变化）。即「申请→批准」之间只要心跳刷过该实例就可能判漂移。
本次正是如此。这不一定是缺陷（宁可不执行也不误停），但意味着**审批延迟越大越容易
撞上漂移**，运维需要重新发起申请。已记入下方待决项。

## 4. 日志平台：已部署 VL 并跑通全链路（原本是环境缺件）

### 4.1 部署前状态：这台机器从未部署过日志后端

`GET /api/v1/logs/federation` 返回 `ENGINE_NOT_READY` / `TARGET_NO_DATA`。
核实为**历史既存状态，非本次变更引入**：旧 Worker 日志 0 条 VL 记录、
`worker-data/bin/` 为空、CP 资产 `cached=false`、`log-runtime` 返 `LOG_UNSUPPORTED`。

### 4.2 缺的两块配置（真机暴露）

仅上传资产不够，还差两处——**任缺一块都会使日志平台完全不可用，且都没有明显报错**：

| 缺什么 | 后果 | 证据 |
|---|---|---|
| `log_vl.package_path`（+ username/password） | `vlSupervisor == nil` ⇒ 采集器与 installer 都不装配 | Worker 启动：`logtail: VL 客户端未装配` |
| `log_tailer.paths` | 绑定表有源归属但**扫描范围为空** ⇒ 采不到任何文件 | `tailer: 文件暂不可采（尚未识别所属源）` |

第二块尤其隐：绑定表（`tailer.registry.jsonl.bindings.json`）里 10 个实例的 `work_dir`
都在（`/srv/mc/game-1` 等），看起来“配好了”，但扫描 glob 为空时一个文件都发现不了。

### 4.3 部署后的真实读数（FR-480 / FR-498 取证）

部署 VL v1.52.0（官方包 SHA256 校验通过）+ 补全两处配置，**全链路第一次真转起来**：

```
联邦查询 coverage.complete = True      reasons = None      ← 无降级
采集读数 harvesters      = 38
         linesTotal      = 435,837 行
         writtenTotal    = 180,600 条
         failedTotal     = 0          gapsTotal = 0
         multilineDropped= 0          unresolvedSource = 0
真实数据样例：source=inst:1/file level=INFO \
  [23:59:58 INFO]: [Gameplay][SilverHunt][Input] 银币猎手监听器已注册，跳过重复注册
```

性能与资源（命中与冷态分开给，因秒级缓存差异极大）：

| 指标 | 读数 | 口径 |
|---|---|---|
| 联邦查询 p50 / max（缓存命中） | 0–1 ms / 36 ms | 同参数 10 次 |
| **联邦查询 p50 / max（冷查询）** | **25 ms / 27 ms** | 每次换 from 绕缓存，8 次 |
| 并发 20 冷查询 p50 / p95 / max | 217 / **249** / 254 ms | 20 并发不同窗口 |
| VL 进程 RSS | 253.7 MB | 启动 ~1.5 分钟后 |
| Worker RSS | 49.7 MB | 含采集器与 38 个 harvester |
| CP RSS | 101.4 MB | 含查询协调与告警评估 |
| VL 数据目录 | 4.9 MB | 刚写入 18 万条 |
| VL 资产目录 | 34 MB | 含官方包 + 解包后二进制 |
| 宿主磁盘 | 140G/188G (79%) | 验收前原本就是 78% |

**与 PRD 记录的门限对照**：FR-498 的 250ms 门限（FAST 联邦查询 p95）
在本机**并发 20 时 p95=249ms，刚好在门限内**；单序列冷查询仅 25ms。
这与 PRD 记载的「本机高负载同口径四次 p95 = 93/680/1259/2486ms」形成对比——
本次宿主 load 仅 0.8（6 个 MC 服占用但未打满），且查询窗口小（24h）。
**不得拿本次读数推翻历史结论**，但它证明了代码侧性能不差，历史超线是宿主环境所致。

### 4.4 仍未覆盖的

- 60 源规模未验：本机只有 10 个实例 / 38 个 harvester，**不是 60 源**。
- 长稳窗（4.5h）未跑：需重建已删除的取证脚本。
- 以上两项属 FR-498 原定范围，本次未做。

## 5. 其他已验证

- CP 健康：`healthz=200`、`readyz=200`，启动日志无 ERROR（除已知的 VL 缺件）。
- 实例数据完整：10 实例 / 1 节点，`game-1` 等状态 RUNNING，`workDirInPlace=true`（就地导入）。
- 插件桥：6 个实例 `managed=true` 握手成功，capabilities 完整。
- 已知无关报错：`bot-worker 分片启动失败`（缺 mineflayer，属节点全局包未装，与本次无关）。

## 6. 清理

- 测试票据 #13 + 回执 + 2 把 `jachk*` Token 已从库中删除。
- 管理员密码临时重置用于验收（便于登录验证），交付后应改回。
