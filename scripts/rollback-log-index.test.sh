#!/bin/sh
#  采集索引回滚脚本的本地契约测试（spec §2.3）：不连接生产主机、不触碰真实数据。
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/scripts/rollback-log-index.sh"
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/jm-rollback-log-index.XXXXXX")
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

fail() {
    echo "测试失败: $*" >&2
    exit 1
}

assert_contains() {
    file=$1
    needle=$2
    grep -Fq -- "$needle" "$file" || fail "$file 缺少: $needle"
}

# --- 夹具：迁移后的现场（索引库 + WAL 伴生文件 + 归档的旧 JSON） -----------
DATA_DIR="$TEST_ROOT/data"
LOG_DIR="$DATA_DIR/var/log"
mkdir -p "$LOG_DIR" "$DATA_DIR/run"
printf 'sqlite-index-placeholder' > "$LOG_DIR/ingest.index.db"
printf 'wal-frames' > "$LOG_DIR/ingest.index.db-wal"
printf 'shm' > "$LOG_DIR/ingest.index.db-shm"
printf '{"sources":{"node:1/g1":{}}}' > "$LOG_DIR/ingest.state.json.migrated"

# --- dry-run 不得改动任何文件 ---------------------------------------------
JM_DATA_DIR="$DATA_DIR" JM_WORKER_SERVICE= sh "$SCRIPT" --dry-run > "$TEST_ROOT/dry-run.log" 2>&1 \
    || fail "dry-run 应当成功退出"
[ -f "$LOG_DIR/ingest.index.db" ] || fail "dry-run 不得移动索引库"
[ -f "$LOG_DIR/ingest.state.json.migrated" ] || fail "dry-run 不得移动归档"

# --- 回滚：停 Worker 用 PID 文件方式，起 Worker 用自定义命令 ---------------
STARTED_FILE="$TEST_ROOT/started"
# PID 文件路径必须与 Worker 实际写入位置一致：`writeWorkerPIDRecord(root.Base(), …)` 写的是
# `<数据根的父目录>/logs/worker.pid`（见 apps/worker/pid_record.go）。此处刻意**不传**
# JM_WORKER_PID_FILE，以检验脚本的默认路径推导；若脚本默认值退回 `<数据根>/run/worker.pid`，
# 下面的回滚会因「缺少 PID 文件」失败——这正是本用例要守的契约。
WORKER_PID_PATH="$(dirname -- "$DATA_DIR")/logs/worker.pid"
mkdir -p "$(dirname -- "$WORKER_PID_PATH")"
# PID 文件指向一个已不存在的进程，脚本应识别为「已停止」并继续。
printf '999999\n' > "$WORKER_PID_PATH"
JM_DATA_DIR="$DATA_DIR" JM_WORKER_SERVICE= \
    JM_WORKER_START_CMD="printf started > '$STARTED_FILE'" \
    sh "$SCRIPT" > "$TEST_ROOT/rollback.log" 2>&1 || fail "回滚应当成功: $(cat "$TEST_ROOT/rollback.log")"

[ -f "$LOG_DIR/ingest.state.json" ] || fail "回滚后旧 JSON 必须回到原路径"
grep -Fq '"sources"' "$LOG_DIR/ingest.state.json" || fail "旧 JSON 内容必须原样"
[ ! -f "$LOG_DIR/ingest.state.json.migrated" ] || fail "归档位置应已清空（内容已改回原路径）"
[ ! -f "$LOG_DIR/ingest.index.db" ] || fail "索引库必须被归档走（不删除）"
[ ! -f "$LOG_DIR/ingest.index.db-wal" ] || fail "索引伴生 WAL 必须一并归档"
[ -f "$STARTED_FILE" ] || fail "回滚后应调用启动命令"
ls "$LOG_DIR"/ingest.index.db.rolled-back.* >/dev/null 2>&1 || fail "必须保留归档的索引库（不删除）"
rollback_log="$TEST_ROOT/rollback.log"
for needle in '已不存在，继续' '归档索引库' '改回 ingest.state.json' '回滚完成'; do
    assert_contains "$rollback_log" "$needle"
done

# --- 幂等/安全门禁：再次回滚必须拒绝（索引库已不存在） --------------------
if JM_DATA_DIR="$DATA_DIR" JM_WORKER_SERVICE= sh "$SCRIPT" > "$TEST_ROOT/again.log" 2>&1; then
    fail "重复回滚必须失败退出"
fi
assert_contains "$TEST_ROOT/again.log" '索引库不存在'

# --- 安全门禁：旧 JSON 已在场时拒绝覆盖 -----------------------------------
printf 'sqlite-index-placeholder' > "$LOG_DIR/ingest.index.db"
printf '{"other":true}' > "$LOG_DIR/ingest.state.json.migrated"
if JM_DATA_DIR="$DATA_DIR" JM_WORKER_SERVICE= sh "$SCRIPT" > "$TEST_ROOT/conflict.log" 2>&1; then
    fail "旧 JSON 已在场时必须拒绝并退出"
fi
assert_contains "$TEST_ROOT/conflict.log" '不覆盖既有数据'
[ -f "$LOG_DIR/ingest.index.db" ] || fail "拒绝回滚时不得移动索引库"
grep -Fq '"sources"' "$LOG_DIR/ingest.state.json" || fail "拒绝回滚时不得改动旧 JSON"

# --- 安全门禁：PID 文件身份与 /proc 不符时必须硬失败（防 PID 复用） --------
#
# 为什么必须守：脚本判定「Worker 已停止」后会立即 `mv` 索引库。若 PID 被系统复用，
# 只凭 `kill -0` 会误判为「Worker 还在跑」而向无关进程发 TERM；更危险的是反向——
# 判定为「已停止」而真实 Worker 仍在写 `ingest.index.db`，rename 后它继续写被改名的文件，
# 新 Worker 拿到空索引库 + 旧 JSON ⇒ 采集账本静默回退、下线窗口内的源位置区间丢失。
# 故此处构造一份**身份必不匹配**的记录（本进程的 PID + 错到不可能的 starttime），
# 断言脚本拒绝处置且**不移动任何文件**。
printf 'sqlite-index-placeholder' > "$LOG_DIR/ingest.index.db"
printf '{"sources":{"node:1/g1":{}}}' > "$LOG_DIR/ingest.state.json.migrated"
rm -f "$LOG_DIR/ingest.state.json"
mkdir -p "$(dirname -- "$WORKER_PID_PATH")"
# 999999 不存在 ⇒ 走「已不存在」分支，不触发身份核验；改用当前 shell 的 PID 制造
# 「进程存在但身份不符」的形态（starttime 填 1，真实值必不相等）。
printf 'pid=%s\nstarttime=1\nexe=/definitely/not/this/binary\ncwd=/definitely/not/this/cwd\n' "$$" > "$WORKER_PID_PATH"
if JM_DATA_DIR="$DATA_DIR" JM_WORKER_SERVICE= sh "$SCRIPT" > "$TEST_ROOT/identity.log" 2>&1; then
    fail "PID 身份不符时必须拒绝处置"
fi
assert_contains "$TEST_ROOT/identity.log" '身份与 /proc'
[ -f "$LOG_DIR/ingest.index.db" ] || fail "身份不符被拒时不得移动索引库"
[ -f "$LOG_DIR/ingest.state.json.migrated" ] || fail "身份不符被拒时不得移动归档"

# --- 缺少迁移归档时拒绝（没有可回滚的旧状态） -----------------------------
rm -f "$LOG_DIR/ingest.state.json" "$LOG_DIR/ingest.state.json.migrated"
if JM_DATA_DIR="$DATA_DIR" JM_WORKER_SERVICE= sh "$SCRIPT" > "$TEST_ROOT/nofile.log" 2>&1; then
    fail "缺少迁移归档时必须拒绝"
fi
assert_contains "$TEST_ROOT/nofile.log" '找不到迁移归档'

# --- 源码契约：脚本只允许移动/改名，不得删除数据 --------------------------
if grep -Eq '^[^#]*rm -[rf]' "$SCRIPT"; then
    fail "回滚脚本不得删除文件（只允许归档/改名）"
fi
assert_contains "$SCRIPT" '不覆盖既有数据'
assert_contains "$SCRIPT" '保留，不删除'

echo '采集索引回滚脚本契约测试通过'
