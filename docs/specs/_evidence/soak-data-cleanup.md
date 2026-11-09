# 压力测试残留数据的清理方法

> 2026-11-09 在 `/srv/jm` 做 FR-498 6 台规模基线 + 4.5h 长稳窗时产生的。
> 本次**没有删除**，原因与将来想清时的做法都记在这里。

## 残留是什么

| 位置 | 内容 | 量级 |
|---|---|---|
| `/srv/mc/<inst>/logs/latest.log` | 每行带 `[Soak]` 标记的测试行 | 6 台共约 **96 万行**（占文件 96%+） |
| VictoriaLogs | 上述行已被采集入库 | 同量级，约 18 MB 磁盘 |

标记形式：`[HH:MM:SS INFO]: [Soak][INFO] [Server thread/INFO]: ...`

## 本次为何不删

1. **日志文件不能截**：6 台 MC 都持有 `latest.log` 的写句柄（`/proc/<pid>/fd` 可验）。
   用 `> file` 截断会让 MC 自己那一边的写入位置错乱，影响它的日志连续性。
2. **VL 删数据不可逆**：受管 VL 默认 `delete.enable=false`（这是有意的：删除没有回头路）。
   为清测试数据而开它，风险大于收益。
3. **量小且会自动过期**：只占 18 MB，VL `retention_period=180d` 到期自动回收；
   日志文件由 MC 自己每天轮转（`logs/` 下可见按日期命名的 `.log.gz`）自然覆盖。

## 将来真想清时

### 日志文件

**不要截断正在写的文件**。两个安全做法：

- 等 MC 自己轮转（最简单，推荐）。轮转后旧内容进 `.log.gz`，`latest.log` 从空开始。
- 若要立即处理，对每个实例执行：`> latest.log` 前先确认 MC 对日志的打开模式是追加
  （Paper 默认是，但**不同服务端/插件可能不同**，改前先验证一台）。

验证标记行是否还在：

```sh
grep -c '\[Soak\]' /srv/mc/*/logs/latest.log
```

### VictoriaLogs

需要两步（且**不可逆**）：

1. 在 `worker.yml` 的 `log_vl` 段加 `delete_enabled: true`，重启 Worker
2. 用 VL 的删除 API 按条件删（LogsQL）：

```sh
# 先算命中量，确认再删
curl -s -u jm:<password> 'http://127.0.0.1:19428/select/logsql/query' \
  -d 'query=_msg:"[Soak]"' -d 'limit=5'

# 真删（不可逆）
curl -s -u jm:<password> 'http://127.0.0.1:19428/delete/logsql' \
  -d 'query=_msg:"[Soak]"'
```

⚠️ 删完记得把 `delete_enabled` 改回 false。

## 如果重做这项验收

**不要往真实实例的 `latest.log` 追写**——本次 2 次 gap 就是这么来的：
注入脚本用 `open(path,"a")` 追写，而 MC 进程同时也在写同一文件，
双写导致 inode 被替换，tailer 按设计报「文件在读完前消失」。

正确做法：用**独立目录** + 在 `log_tailer.paths` 里额外加一条 glob，
让测试数据走同一条采集链路但与生产日志物理隔离。
