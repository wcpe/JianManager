# FR-498 的 250ms 门限测量仪（入库、可复算）

## 为什么这份工具现在才算「入库」

它原本在 `.tmp/search-p95-harness-20261026/`——**`.tmp/` 被 gitignore**。
本仓已经因为这个吃过一次亏：长稳窗的 harness 丢在 `/tmp` 里，PRD 的
「harness 需用当前 HEAD 重建重跑」挂了很久（2026-11-04 才补上 `scripts/soak-window/`）。
同一类错误不该犯第二次，所以把测量仪先收进仓库。

## 当前状态（2026-11-04）

**250ms 门限已用户裁定为「本机无法判定」**（选项 B，见
`docs/specs/log-verify-chunking/spec.md` §5.2a）。本工具**未被使用**于得出该裁定，
它是为将来可能的「换空闲机重跑」（选项 A）或「查长尾」（选项 C）准备的。

## 口径（改动即失效，必须先裁定）

```
RETENTION_SLOTS = 8      查询槽数（--slots 非 8 直接拒绝执行）
BACKTRACK_SEC   = 3.0    回溯窗
LIMIT           = 200    每页条数
门限 250ms               **不在脚本内定义**（脚本只测量与如实报告）
```

分桶 `fast` / `complete` / `error`：规格明确「首屏 FAST Pending 不充作完整 Search 样本」，
**fast 桶的数字不得当作完整 Search 成绩**。样本为 0 时输出 `UNDETERMINED`，绝不当通过。

## 用法

```bash
python3 scripts/search-p95/search_p95.py \
  --base http://127.0.0.1:19000 \
  --token-file /path/to/token \
  --duration 30 \
  --out /tmp/p95.json
```

## 先决条件（缺一样得出的都是假读数）

1. **宿主空闲**：`/proc/loadavg` 会被记录到输出里供复核。2026-10-26 那份 HANDOFF 的
   核心结论是：同一口径四次测量 p95 = 93 / 680 / 1259 / 2486 ms（极差 26.7 倍），
   达标与不达标两种结论都能从同一台机器里「测」出来 —— 所以非空闲机器上的读数是废的。
2. **干净 CP 数据库**（否则离线节点污染授权 target 集合，`partial_reasons` 恒含 `OFFLINE`）
3. **干净 VL 数据目录**（残留 `flock.lock` 会让 VL 启动 panic，投递全失败）
4. **数据量对齐**：预热共 64000 条总量（不是每源 1 万行）

完整环境搭建步骤与四个已踩过的坑见 `HANDOFF-20261026.md` 第三节/第四节。

## 已知未解释现象

`p50 = 10.6ms` 而 `max = 1692ms`，长尾显著但**未归因**。
若走选项 C，从这里开始：不得只凭墙钟推断热点。
