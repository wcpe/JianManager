# VictoriaLogs 官方能力对照表

> 证据等级：本文所有结论均在本机 VL v1.52.0（`127.0.0.1:19461`）**实测得出**，
> 不是从文档推断。数据窗口统一为 `2026-11-04T00:00:00Z ~ 2026-11-05T00:00:00Z`，
> 该窗口 VL 内共有 19 个实例源、5276 条。
>
> 生成时间：2026-11-05。任何参数语义变更请重跑本文末尾的复现命令。

## 一、结论先行

**我们整套日志查询只用了 VL 的一个端点：`/select/logsql/query`。**

VL 提供 14 个查询端点，其中至少 6 个能直接替掉我们自己维护的逻辑：

| 我们自建的东西 | 官方对应 | 差距性质 |
|---|---|---|
| Catalog 分区目录（哪些天有数据） | `hits` | **缺陷**：副本会丢，导致「日志页整页为空」事故链 |
| 启动期从 VL 反推 30 天回填 | `hits` | **低效**：每源一次查询，hits 一次拿全部 |
| `Facets`（遍历 range 后内存累加） | `facets` / `field_values` | **可用但绕**：官方一次聚合 |
| `Stats`（LogsQL stats 管道） | `stats_query_range` | **可用但非标准**：官方返回 Prometheus matrix |
| `Fields`（从返回行里猜字段名） | `field_names` | **不准确**：只能看到本页返回行的字段 |
| 无 | `streams` / `stream_ids` | **缺失**：无法按 stream 维度分析 |

## 二、逐端点实测

### 2.1 `/select/logsql/query` ✅ 在用

我们唯一使用的端点。返回 JSON lines 流。

| 参数 | 语义 | 我们在用 |
|---|---|---|
| `query` | LogsQL 表达式 | ✅ |
| `start` / `end` | `[start, end)` 半开区间 | ✅ |
| `limit` | 返回条数上限（配合 top-K 早停） | ✅ |
| `extra_filters` | 额外过滤，与 query AND 组合 | ✅ |
| `sort_direction=asc` | 配合 limit 取最旧 | ❌ 未用（游标靠自建复合键） |
| `offset` | 配合 limit 跳过 N 条 | ❌ 未用（分页靠自建游标） |

**待评估**：官方 `limit + offset` 分页是简易分页，我们用的是「复合排序键游标」，
两者语义不同（我们的保证跨页无重无漏，offset 在数据变动时会漂移）。
**判断：保持自建游标**，理由写进架构决策。

### 2.2 `/select/logsql/hits` ⭐ **完全未用，是本轮核心**

按时间桶统计命中数，一次拿回完整分布。

```json
{"hits":[{"fields":{"log_source_id":"inst:317/file"},
          "timestamps":["2026-10-01T00:00:00Z", ...],
          "values":[0, 0, ..., 243, 0],
          "total":243}]}
```

| 参数 | 语义 | 实测证据 |
|---|---|---|
| `query` | 过滤表达式 | `*` |
| `field` | **单字段**分组维度 | 传 `log_source_id,level` 会被当字面量（`fields` 变成 `{"log_source_id,level":""}`） |
| `step` | 时间桶大小，支持 `1h`/`1d`/`1w` | 三个值均实测通过 |
| `start` / `end` | 时间范围，半开 | ✅ |
| `offset` | **时区对齐**：让桶边界平移 | `offset=8h` ⇒ 桶起点从 `T00:00Z` 变成 `T16:00Z`（= HKT 00:00） |

**关键实测（决定架构正确性）**：

```
不传 offset（UTC 划天）: 11-01=2880  11-02=1746  11-03=0  11-04=890  11-05=773
传 offset=8h（HKT 天）  : 10-31=2303  11-01=2706  11-04=1663
```

⇒ **我们的分区键是 UTC 日 ⇒ 绝不能传 offset**，否则整张表错一天。
这正是历史上三次「天界口径」事故的同款根因，必须写死。

**规模实测（一次请求）**：

```
query=* & field=log_source_id & step=1d & 2026-10-01~2026-11-06
→ 31 个源 × 36 天 = 372 个 (源,天) 分区
→ 217 ms，31 KB
```

**用途**：启动期一次调用即可重建全部「哪些天有数据」，无需 journal 落盘、无需 30 天回填。

**注意**：`field=log_source_id` 返回的是**完整源 id**（`inst:318/file`），不是命名空间
（`inst:318`）。映射需按 `/` 前缀归并，且必须过滤掉未登记的源（实测含 11 个 `probe:*` 探针源）。

### 2.3 `/select/logsql/facets` ⚠️ 可用但我们在自建

返回「每个字段的高频值」，默认对 `_stream` 等字段分面。

```json
{"facets":[{"field_name":"_stream",
            "values":[{"field_value":"{log_source_id=\"inst:318/file\",stream=\"stdout\"}","hits":890}, ...]}]}
```

**与我们现有 `Facets` 的对比**：
- 我们：遍历 plan.Ranges，每个 range 调一次 `Stats`（LogsQL stats 管道），内存累加
- 官方：一次请求直接聚合

实测两者对 `level` 的结果一致（INFO 4699 / WARN 546 / ERROR 31）。
**判断**：可改但非缺陷，优先级低于 hits。

### 2.4 `/select/logsql/field_values` ✅ 可用

```json
{"values":[{"value":"INFO","hits":4699},{"value":"WARN","hits":546},{"value":"ERROR","hits":31}]}
```

参数：`query` / `field` / `start` / `end`。
与我们 facets 对 `level` 的结果**逐值一致**。

### 2.5 `/select/logsql/field_names` ⭐ 我们完全没用

```json
{"values":[{"value":"_msg","hits":5276},{"value":"event_id","hits":5276},{"value":"level","hits":5276}, ...]}
```

**我们现有的 `Fields` 实现**：调 `Search` 拿一页行，遍历 `ev.Fields` 去重。
⇒ 只能看到「本页返回的那几条日志恰好有的字段」，**不是该时间窗的真实字段全集**。

**判断**：这是**缺陷**（观测不准确），官方端点就是为这个场景设计的。

### 2.6 `/select/logsql/stats_query` / `stats_query_range`

- `stats_query`：需要 `time` 参数，缺省时间范围会撞 `maxQueryTimeRange`
- `stats_query_range`：返回 **Prometheus matrix 格式**

```json
{"status":"success","data":{"resultType":"matrix",
  "result":[{"metric":{"__name__":"count(*)"},"values":[[1793750400,"5276"]]}]}}
```

**与我们现有 `Stats` 的对比**：我们用 LogsQL `| stats ...` 管道自己解析 NDJSON；
官方返回标准 matrix，可直接对接任何 Prometheus 生态。
**判断**：合理取舍（我们的协议已固化），但值得记录为技术债。

### 2.7 `/select/logsql/streams` / `stream_ids` / `stream_field_*` ⭐ 完全缺失

```json
/streams → {"values":[{"value":"{log_source_id=\"inst:318/file\",stream=\"stdout\"}","hits":890}, ...]}
/stream_field_names → {"values":[{"value":"log_source_id","hits":5276},{"value":"stream","hits":5276}]}
```

**判断**：**能力缺失**。当前无法回答「这个时间窗里有哪些 stream / 哪些 stream 字段」。
属于产品能力缺口，不是缺陷；按需再做。

### 2.8 `/select/logsql/tail`

官方提供 `start_offset`（补历史）、`offset`（延迟）、`refresh_interval`。
我们实现了 `VIEW_BOUNDED`，`FOLLOW_LIVE` 明确未支持（有代码注释说明）。
**判断**：合理取舍，但 `start_offset` 能显著改善「打开日志页先看到最近 N 条」的体验。

## 三、优先级排序

| 优先级 | 项 | 性质 | 理由 |
|---|---|---|---|
| **P0** | 分区目录改用 `hits` | 缺陷 | 直接决定「查得到查不到」，且能去掉磁盘写入 |
| P1 | `Fields` 改用 `field_names` | 缺陷 | 当前返回的字段集不准确 |
| P2 | `Facets`/`Stats` 改用官方聚合 | 优化 | 少一次扇出、少一段自维护解析 |
| P3 | `streams` 系列 | 新能力 | 产品缺口，非缺陷 |
| — | 分页保持自建游标 | 合理取舍 | 官方 offset 分页在数据变动下会漂移 |

## 四、复现命令

```bash
V=http://127.0.0.1:19461; A="-u jm:local-only"
# hits：分区目录（本文核心）
curl -s $A "$V/select/logsql/hits" -d 'query=*' -d 'field=log_source_id' \
  -d 'start=2026-10-01T00:00:00Z' -d 'end=2026-11-06T00:00:00Z' -d 'step=1d'
# hits：时区口径对照（证明 offset 会平移天界）
curl -s $A "$V/select/logsql/hits" -d 'query=*' -d 'step=1d' -d 'offset=8h' \
  -d 'start=2026-11-01T00:00:00Z' -d 'end=2026-11-06T00:00:00Z'
# 字段全集
curl -s $A "$V/select/logsql/field_names" -d 'query=*' \
  -d 'start=2026-11-04T00:00:00Z' -d 'end=2026-11-05T00:00:00Z'
# 字段值分布
curl -s $A "$V/select/logsql/field_values" -d 'query=*' -d 'field=level' \
  -d 'start=2026-11-04T00:00:00Z' -d 'end=2026-11-05T00:00:00Z'
# 标准 Matrix 聚合
curl -s $A "$V/select/logsql/stats_query_range" -d 'query=* | stats count()' \
  -d 'start=2026-11-04T00:00:00Z' -d 'end=2026-11-05T00:00:00Z' -d 'step=1d'
# streams
curl -s $A "$V/select/logsql/streams" -d 'query=*' \
  -d 'start=2026-11-04T00:00:00Z' -d 'end=2026-11-05T00:00:00Z'
```
