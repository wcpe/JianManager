# VictoriaLogs 手工排障探针指南

> 适用：用 `curl` 直接对受管 VL（默认 `127.0.0.1:19461`）做手工读写，验证「数据到底在不在」。
>
> **为什么需要这份文档**：2026-11-05 排查日志页空白时，团队连续踩了三次探针陷阱，
> 每次都得出过相反的结论（「VL 拒收未来时间戳」/「VL 静默丢弃」/「探针方法有问题」）。
> 三个陷阱都会让**成功的写入看起来像失败**，从而把排查引向完全错误的方向。

## 铁律：`HTTP 200` 不等于写入成功

`/insert/jsonline` 在**解析不出任何行**时仍然返回 `200`。
所以「写完了没报错」这件事本身**不构成任何证据**。

唯一可信的判据是：**写完之后，用查询把它读回来**。

---

## 陷阱 1：缺 `Content-Type` ⇒ 200 且零写入

`curl` 默认发 `Content-Type: application/x-www-form-urlencoded`，
VL 会把它当表单而不是 jsonline 解析 ⇒ 一行都解析不出 ⇒ 返回 200、什么都没写。

**必须显式带 JSON 类型**（以下三种实测都可用）：

```bash
-H 'Content-Type: application/stream+json'      # 生产客户端用的就是这个
-H 'Content-Type: application/x-ndjson'
-H 'Content-Type: application/json'
```

实测对照（2026-11-05，生产 VL v1.52.0，同时刻同 body 只差 header）：

| Content-Type | HTTP | 落库 |
|---|---|---|
| 不带（curl 默认） | 200 | **0 条** |
| `application/stream+json` | 200 | 1 条 |
| `application/x-ndjson` | 200 | 1 条 |
| `application/json` | 200 | 1 条 |

> 生产投递路径**不受影响**：`vlsup.Client.insertOnce` 显式设了
> `Content-Type: application/stream+json`（见 `internal/worker/logs/vlsup/client.go`）。
> 本陷阱只咬手工 curl。

## 陷阱 2：查询不给显式 `end` ⇒ 400，而不是「没数据」

```bash
# ❌ 只给 start 会返回 400（too big time range），不是空结果
curl -u jm:local-only '.../select/logsql/query' -d 'query=...' -d 'start=-10m'
```

VL 的 `-search.maxQueryTimeRange=4344h`（31d）；只给 `start` 时 `end` 默认成
时间类型最大值 ⇒ 区间远超上限 ⇒ **HTTP 400**。

**危险之处**：脚本若把非 2xx 的响应体当空结果处理，会得出「查不到 ⇒ 数据丢了」
这个**完全错误**的结论。

```bash
# ✅ 必须同时给 start 和 end
curl -u jm:local-only --get 'http://127.0.0.1:19461/select/logsql/query' \
  --data-urlencode 'query=log_source_id:="inst:318/file" | stats count()' \
  --data-urlencode 'start=2026-11-05T00:00:00Z' \
  --data-urlencode 'end=2026-11-05T02:00:00Z'
```

## 陷阱 3：写完立刻查 ⇒ 看起来「没写进去」

VL 从写入到可查询有**可见延迟**。实测带正确 header 时 10s 内可见，
但**不要假设 10s 是上界**——摄取链路、分段 flush、负载都会影响它。

**判据**：写入后**至少等 60s** 再查询，并**用独立的源名**，避免与历史探针混在一起。

```bash
MARK="probe:$(date +%s)"
curl -s -o /dev/null -w 'write=%{http_code}\n' \
  -u jm:local-only -X POST 'http://127.0.0.1:19461/insert/jsonline' \
  -H 'Content-Type: application/stream+json' \
  --data-binary "{\"_msg\":\"probe\",\"_time\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"event_id\":\"$MARK\",\"log_source_id\":\"$MARK\"}"
sleep 60
curl -s -u jm:local-only --get 'http://127.0.0.1:19461/select/logsql/query' \
  --data-urlencode "query=log_source_id:=\"$MARK\" | stats count()" \
  --data-urlencode 'start=-30m' --data-urlencode 'end=+30m'
```

---

## 附一：`max(_time)` 不能用来推断「写入被拒」

排查时容易这样推：写完 → 查不到 → 看全库 `max(_time)` 没前进 → **断言「写入被时间窗拒了」**。

这是**错的**。`max(_time)` 是「已有数据的最新时间」，探针若压根没写进去
（陷阱 1），它当然不会前进。**「读数没动」与「写入被拒」是两件事**，
必须先用能自证的探针区分开。

## 附二：VL 不按 `event_id` 去重

同 `_time` / 同 `event_id` / 同 `_msg` 写两次就是**两行**（实测 `count()=2`，
且两行 `_stream_id` 相同）。VictoriaLogs 是 append-only 时序库，
`/insert/jsonline` 不做任何字段级唯一约束。

⇒ 任何「反正下游会按 event_id 去重」的假设都不成立。
详见 `docs/specs/worker-log-platform-contract/spec.md` §4 的订正说明。
