#!/usr/bin/env bash
#
# FR-498 长稳窗口的分段执行器（入库、可复算）。
#
# # 为什么要入库
#
# 2026-11-04 之前的历次长稳窗都跑在 /tmp 里的临时脚本上，跑完就丢。
# 结果是 PRD 里「harness 需用当前 HEAD 重建重跑」这条长期挂着——
# 不是没能力跑，是**跑过的那套东西没留下来**。同 `scripts/capacity-load/` 的动机。
#
# # 为什么要分段
#
# 9 × 30min = 4.5 小时。单进程跑完的话：
#   - 任何一次 panic / 超时都会丢掉全部读数；
#   - 一个慢泄漏要在 4.5 小时后才能被看见，中间无法判断；
#   - 单条 `go test -timeout` 也要跟着拉到 5 小时以上。
# 分段后每段是独立进程、独立 t.TempDir()、独立日志：**某一段失败只丢那一段**，
# 其余读数仍成立。这也让「段间趋势」（跨段堆是否单调拾高）成为一个额外判据。
#
# # 用法
#
#   scripts/soak-window/run.sh                       # 默认 9 段 × 30min（完整窗口）
#   SOAK_SEGMENTS=2 SOAK_ROUNDS=60 scripts/soak-window/run.sh   # 冒烟（2 段 × 1min）
#
# 环境变量：
#   SOAK_SEGMENTS  段数，默认 9
#   SOAK_ROUNDS    每段轮数，默认 1800（× SOAK_INTERVAL=1s ⇒ 30min）
#   SOAK_INTERVAL  每轮间隔，默认 1s  ⇒ 每源 1 行/s（保守于容量基线的 30 行/s）
#   SOAK_SOURCES   源数，默认 60（对齐 FR-498 口径）
#   SOAK_VL_URL    VictoriaLogs 地址；不设则只测采集侧（不覆盖投递，不推荐用于长稳）
#   SOAK_OUT       输出目录，默认 /tmp/rt/soak-win
#
# # 宿主前提（写在这里以免后人误读读数）
#
# 本脚本**不做独占检测**——它没法知道机器上还跑着什么。
# 跑前请自行确认：
#   1. `uptime` 的 1 分钟负载在你的评估阈值内（本仓 2026-11-04 实测 13–16 / 40 核，
#      同时有其它工程的 Java 进程占 50%+ CPU ⇒ 那一轮读数**带噪声**，跨段比趋势可以，
#      绝对值不可与静默机器直接比）；
#   2. 没有并发跑其它 `ingest.test`（GO 测试二进制同名，互相干扰会让两边读数全部作废）；
#   3. `/tmp` 所在分区有足够空间（每段会建 60 个源文件并持续追加）。
#
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SEGMENTS="${SOAK_SEGMENTS:-9}"
ROUNDS="${SOAK_ROUNDS:-1800}"
INTERVAL="${SOAK_INTERVAL:-1s}"
SOURCES="${SOAK_SOURCES:-60}"
OUT="${SOAK_OUT:-/tmp/rt/soak-win}"
VL="${SOAK_VL_URL:-}"

mkdir -p "$OUT"

# 宿主前提③：不得有并发的 ingest.test。
#
# 必须写在**脚本里**而不是调用方的命令行里：`ps aux | grep -q '[i]ngest.test'` 这种写法
# 在 shell 里很容易自匹配——只要调用方那行命令（含 echo 文案）里出现 `ingest.test` 字面，
# ps 就会把它自己的 `/bin/sh -c '...'` 也列出来并命中，于是永远判定「有遗留」。
# 2026-11-04 就是这么误报了一次。脚本文件的正文不会出现在 ps 的 argv 里，所以这里安全。
if ps aux | grep -q '[i]ngest\.test'; then
  echo "拒绝开窗：已有 ingest.test 在跑（并发跑会让两边读数全部作废）"
  ps aux | grep '[i]ngest\.test' | sed 's/^/  /'
  exit 2
fi
BIN="$OUT/ingest.test"
SUMMARY="$OUT/summary.log"
PROGRESS="$OUT/progress.log"

# 每次开窗先清掉旧的汇总，避免上一轮的读数混进本轮。
: > "$SUMMARY"
: > "$PROGRESS"

# 预先把测试二进制编出来：
#   ① 避免每段都重编（9 段 × 编译时间会把窗口拉长）；
#   ② 也避免「某一段刚好碰到代码改动所以编了个不同的二进制」——
#      9 段必须跑**同一个**二进制，否则跨段趋势没有意义。
# 这也是 PRD 「harness 需用当前 HEAD 重建重跑」的落点：HEAD 变了就重编。
{
  echo "=== 构建测试二进制 ==="
  cd "$ROOT" || exit 1
  go test -c -o "$BIN" ./internal/worker/logs/ingest/ || { echo "构建失败，中止"; exit 1; }
  echo "二进制: $BIN"
  echo "HEAD:   $(git rev-parse --short HEAD 2>/dev/null || echo '<非 git>')"
  echo "段数=$SEGMENTS 每段=$ROUNDS 轮 × $INTERVAL 源数=$SOURCES VL=${VL:-<未设>}"
  echo
} | tee -a "$PROGRESS"

START_ALL=$(date +%s)
FAILED=0
for i in $(seq 1 "$SEGMENTS"); do
  echo "[seg$i/$SEGMENTS] START $(date +%H:%M:%S)" | tee -a "$PROGRESS"

  # 每段独立目录：测试自己用 t.TempDir()，但工作目录独立能避免相对路径互相干扰。
  SEGDIR="$OUT/seg$i"
  mkdir -p "$SEGDIR"

  (
    cd "$SEGDIR" || exit 1
    if [ -n "$VL" ]; then
      SOAK_VL_URL="$VL" SOAK_ROUNDS="$ROUNDS" SOAK_INTERVAL="$INTERVAL" SOAK_SOURCES="$SOURCES" \
        "$BIN" -test.run 'TestSoakWindow60Sources' -test.v -test.timeout 60m \
        > "$OUT/seg-$i.log" 2>&1
    else
      SOAK_ROUNDS="$ROUNDS" SOAK_INTERVAL="$INTERVAL" SOAK_SOURCES="$SOURCES" \
        "$BIN" -test.run 'TestSoakWindow60Sources' -test.v -test.timeout 60m \
        > "$OUT/seg-$i.log" 2>&1
    fi
  )
  RC=$?

  if [ "$RC" -ne 0 ]; then
    FAILED=$((FAILED + 1))
  fi
  echo "[seg$i/$SEGMENTS] END $(date +%H:%M:%S) rc=$RC" | tee -a "$PROGRESS"

  # 汇总：只抽判据行，不把整份日志塞进 summary。
  # 用 grep -E + 前缀而不是「全拷」——summary 要能被一眼读完。
  {
    echo "--- seg$i (rc=$RC) ---"
    grep -E '堆前1/3|WAL前1/3|单位积压|poll 延迟|总运行|^--- (PASS|FAIL)' "$OUT/seg-$i.log" \
      | sed 's/^/  /' || echo "  <无判据行：本段可能未跑到判据就退出了>"
  } >> "$SUMMARY"

  # VL 总量快照：跨段单调增长是「投递真的在跑」的旁证。
  if [ -n "$VL" ]; then
    TOTAL=$(curl -s --max-time 10 "$VL/select/logsql/query" \
      --data-urlencode 'query=* | stats count() as c' -d 'start=-12h' 2>/dev/null \
      | grep -o '"c":"[0-9]*"' | head -1 | grep -o '[0-9]*')
    echo "  VL 总数=${TOTAL:-<查询失败>}" >> "$SUMMARY"
  fi
  echo >> "$SUMMARY"

done

ELAPSED=$(( $(date +%s) - START_ALL ))
echo "ALL DONE $(date +%H:%M:%S) 耗时=${ELAPSED}s 失败段=$FAILED/$SEGMENTS" | tee -a "$PROGRESS"
echo "汇总: $SUMMARY" | tee -a "$PROGRESS"

# 退出码只表达「有没有段失败」，不表达「长稳是否判红」——后者看 summary 里的判据行。
[ "$FAILED" -eq 0 ]
