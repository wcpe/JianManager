#!/usr/bin/env bash
# vl-insert-probe.sh — VictoriaLogs 写入自证探针
#
# 目的：回答「这行数据到底有没有写进 VL」。
#
# ## 为什么需要它
#
# `/insert/jsonline` 会在多种情况下返回 **HTTP 200 但静默丢弃整批**。
# 因此「200」绝不能当作「写入成功」——必须写进去再查回来。
#
# ## 两个已知的陷阱（都会让人得出完全相反的结论）
#
#   1. **缺 `Content-Type`**：curl 默认 `application/x-www-form-urlencoded`，
#      VL 会把它当表单解析 ⇒ 解析不出行 ⇒ 200 且零写入。
#      本脚本每个用例都用**独立** `log_source_id`，并显式给 JSON Content-Type。
#   2. **查询不给显式 `end`**：VL 会把 end 默认成 2262 年，然后以 400 拒绝
#      （`too big time range selected`）。若把 400 当"查不到"，结论就反了。
#      本脚本始终显式给 start/end，并把查询错误与「0 行」严格区分开。
#
# ## 用法
#
#   ./scripts/vl-insert-probe.sh --url http://127.0.0.1:19461 \
#                                --user jm --pass "$VL_PASSWORD" \
#                                [--wait 60] [--cases withct,noct]
#
# `--cases` 默认跑两组对照：
#   withct = 带 `Content-Type: application/stream+json`  （期望可见）
#   noct   = 不带 Content-Type                            （观察项，不计失败）
#
# 输出：每个用例一行结论（VISIBLE / DROPPED / QUERY_ERROR），最后一行 SUMMARY。
# 退出码：0 = 所有期望成立的用例都可见；1 = 有期望之外的失败。
#
# ## 为什么每个 case 用独立 log_source_id
#
# 同一个源名下多次写入会互相淹没（计数对不上时无法归因）。
# 独立源名让「这个 case 到底落了几行」成为可判定的事实。
#
# ## 为什么必须等一个明确窗口再查
#
# VL 写入后有个可见延迟（本仓实测约 1s 量级，但会随负载浮动）。
# 写完立刻查会得到「0 行」，从而把「还没可见」误判成「被丢弃」。
# 默认等 60s，足以覆盖正常延迟；要更严格就调大。
set -uo pipefail

URL=""
USER=""
PASS=""
WAIT=60
CASES="withct,noct"
SAMPLE_MSG="vl-insert-probe"

while [ $# -gt 0 ]; do
  case "$1" in
    --url)   URL="$2"; shift 2 ;;
    --user)  USER="$2"; shift 2 ;;
    --pass)  PASS="$2"; shift 2 ;;
    --wait)  WAIT="$2"; shift 2 ;;
    --cases) CASES="$2"; shift 2 ;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$URL" ]; then echo "missing --url" >&2; exit 2; fi

AUTH=()
if [ -n "$USER" ]; then AUTH=(-u "$USER:$PASS"); fi

time_utc() { # $1 = offset spec for date, e.g. "-1 minute"
  date -u -d "$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ
}

NOW_T=$(time_utc '-1 minute')
# 查询窗口留足前后余量，并且**始终显式给 end**。
Q_START=$(time_utc "-$((WAIT + 600)) seconds")
Q_END=$(time_utc "+$((WAIT + 3600)) seconds")

# count_for 打印整数行数；查询失败时打印 ERR:<body>（绝不把错误当 0）。
count_for() {
  local out
  out=$(curl -sS -m 15 "${AUTH[@]}" --get "$URL/select/logsql/query" \
        --data-urlencode "query=log_source_id:=$1 | stats count()" \
        --data-urlencode "start=$Q_START" \
        --data-urlencode "end=$Q_END" 2>&1)
  case "$out" in
    *'"count(*)"'*) printf '%s' "$out" | sed -n 's/.*"count(\*)"[":]*\([0-9]*\).*/\1/p' ;;
    *) printf 'ERR:%s' "$out" ;;
  esac
}

STAMP=$(date +%s%N)
RESULT_NAMES=()

for CASE in $(printf '%s' "$CASES" | tr ',' ' '); do
  SRC="probe:$CASE-$STAMP"
  BODY=$(printf '{"_time":"%s","event_id":"%s-e1","log_source_id":"%s","source_generation":"g1","_msg":"%s","level":"INFO","stream":"stdout"}' \
          "$NOW_T" "$CASE" "$SRC" "$SAMPLE_MSG")

  if [ "$CASE" = "noct" ]; then
    # 刻意**不带** Content-Type（curl 会补 application/x-www-form-urlencoded）
    CODE=$(curl -sS -m 10 "${AUTH[@]}" -X POST "$URL/insert/jsonline" \
           --data-binary "$BODY" -o /dev/null -w '%{http_code}' 2>&1)
  else
    CODE=$(curl -sS -m 10 "${AUTH[@]}" -X POST "$URL/insert/jsonline" \
           -H 'Content-Type: application/stream+json' \
           --data-binary "$BODY" -o /dev/null -w '%{http_code}' 2>&1)
  fi
  echo "case=$CASE src=$SRC insert_http=$CODE"
  RESULT_NAMES+=("$CASE|$SRC")
done

echo "waiting ${WAIT}s for visibility ..."
sleep "$WAIT"

FAILED=0
for item in "${RESULT_NAMES[@]}"; do
  CASE="${item%%|*}"; SRC="${item##*|}"
  C=$(count_for "$SRC")
  if [ "${C#ERR:}" != "$C" ]; then
    echo "case=$CASE src=$SRC VERDICT=QUERY_ERROR ($C)"
    FAILED=1
    continue
  fi
  if [ "$C" -gt 0 ] 2>/dev/null; then
    echo "case=$CASE src=$SRC rows=$C VERDICT=VISIBLE"
  else
    echo "case=$CASE src=$SRC rows=0 VERDICT=DROPPED"
    # 只有「带 Content-Type」的 case 被丢才算失败；
    # 不带 header 被丢正是本脚本要演示的现象，记为观察项。
    if [ "$CASE" != "noct" ]; then FAILED=1; fi
  fi
done

echo "SUMMARY wait=${WAIT}s cases=$CASES failed=$FAILED"
exit "$FAILED"
