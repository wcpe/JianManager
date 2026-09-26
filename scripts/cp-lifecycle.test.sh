#!/bin/sh
# Control Plane 生命周期脚本的 Linux shell contract 测试，不连接生产主机。
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/scripts/cp-lifecycle.sh"
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/jm-cp-lifecycle.XXXXXX")
INSTALL_DIR="$TEST_ROOT/install"
BIN="$INSTALL_DIR/fake-cp.sh"
CONFIG="$INSTALL_DIR/control-plane.yml"
PID_FILE="$INSTALL_DIR/run/control-plane.pid"
LOCK_DIR="$INSTALL_DIR/run/control-plane.lock"
OWNER_FILE="$TEST_ROOT/owner.pid"
CHILD_FILE="$TEST_ROOT/child.pid"
STARTED_FILE="$TEST_ROOT/started"
RELEASE_FILE="$TEST_ROOT/release"
IGNORE_TERM_FILE="$TEST_ROOT/ignore-term"
MUTATE_TERM_FILE="$TEST_ROOT/mutate-term"
FAKE_BIN_DIR="$TEST_ROOT/bin"
EXPECTED_EXE=$(readlink -f /bin/sh 2>/dev/null || printf '%s\n' /bin/sh)

cleanup_process() {
    if [ -f "$PID_FILE" ]; then
        pid=$(awk -F= '$1 == "pid" { print $2; exit }' "$PID_FILE" 2>/dev/null || true)
        case "$pid" in ''|*[!0-9]*) : ;; *) kill -KILL "$pid" 2>/dev/null || true ;; esac
    fi
    if [ -f "$CHILD_FILE" ]; then
        child=$(awk 'NR == 1 { print; exit }' "$CHILD_FILE" 2>/dev/null || true)
        case "$child" in ''|*[!0-9]*) : ;; *) kill -KILL "$child" 2>/dev/null || true ;; esac
    fi
}
cleanup() {
    if [ "${KEEP_TEST_ROOT:-0}" = 1 ]; then
        printf '保留测试目录: %s\n' "$TEST_ROOT" >&2
    else
        cleanup_process
        rm -rf "$TEST_ROOT"
    fi
}
trap cleanup EXIT HUP INT TERM

fail() {
    echo "测试失败: $*" >&2
    exit 1
}

assert_file() {
    [ -f "$1" ] || fail "缺少文件: $1"
}

assert_not_file() {
    [ ! -e "$1" ] || fail "文件不应存在: $1"
}

assert_eq() {
    [ "$1" = "$2" ] || fail "期望 [$1]，实际 [$2]"
}

assert_contains() {
    grep -F -- "$2" "$1" >/dev/null || fail "文件 $1 未包含: $2"
}

assert_command_fails() {
    if "$@" >"$TEST_ROOT/command.out" 2>&1; then
        fail "命令本应失败: $*"
    fi
}

run_script() {
    JM_CP_INSTALL_DIR="$INSTALL_DIR" \
    JM_CP_BINARY="$BIN" \
    JM_CP_CONFIG="${TEST_CONFIG:-$CONFIG}" \
    JM_CP_PID_FILE="$PID_FILE" \
    JM_CP_LOCK_DIR="$LOCK_DIR" \
    JM_CP_LOG_FILE="$INSTALL_DIR/run/cp.log" \
    JM_CP_EXPECTED_EXE="$EXPECTED_EXE" \
    JM_CP_START_TIMEOUT="${JM_CP_START_TIMEOUT:-8}" \
    JM_CP_STOP_TIMEOUT="${JM_CP_STOP_TIMEOUT:-4}" \
    JM_CP_POLL_INTERVAL="${JM_CP_POLL_INTERVAL:-1}" \
    JM_TEST_OWNER_FILE="$OWNER_FILE" \
    JM_TEST_CHILD_FILE="$CHILD_FILE" \
    JM_TEST_DELAY_START="${JM_TEST_DELAY_START:-0}" \
    JM_TEST_STARTED_FILE="${JM_TEST_STARTED_FILE:-$STARTED_FILE}" \
    JM_TEST_RELEASE_FILE="${JM_TEST_RELEASE_FILE:-$RELEASE_FILE}" \
    JM_TEST_IGNORE_TERM_FILE="$IGNORE_TERM_FILE" \
    JM_TEST_MUTATE_TERM_FILE="$MUTATE_TERM_FILE" \
    JM_TEST_PID_FILE="$PID_FILE" \
    PATH="$FAKE_BIN_DIR:/usr/bin:/bin" \
    "$SCRIPT" "$@"
}

prepare_files() {
    mkdir -p "$INSTALL_DIR/current" "$FAKE_BIN_DIR"
    cat >"$CONFIG" <<'YAML'
server:
  host: 127.0.0.1
  port: 18080
grpc:
  port: 19100
YAML
    cat >"$BIN" <<'SH'
#!/bin/sh
# 测试用长驻进程：记录自身 PID，支持延迟就绪与忽略 TERM。
child=
cleanup_child() {
    if [ -n "$child" ]; then
        kill "$child" 2>/dev/null || true
    fi
    rm -f "$JM_TEST_OWNER_FILE" "$JM_TEST_CHILD_FILE"
    exit 0
}
handle_signal() {
    if [ -f "${JM_TEST_IGNORE_TERM_FILE:-}" ]; then
        if [ -f "${JM_TEST_MUTATE_TERM_FILE:-}" ]; then
            awk -F= 'BEGIN { OFS="=" } $1 == "starttime" { print $1, 1; next } { print }' "$JM_TEST_PID_FILE" >"$JM_TEST_PID_FILE.mutated"
            mv -f "$JM_TEST_PID_FILE.mutated" "$JM_TEST_PID_FILE"
        fi
    else
        cleanup_child
    fi
}
trap handle_signal TERM INT
if [ "${JM_TEST_DELAY_START:-0}" = 1 ]; then
    : >"$JM_TEST_STARTED_FILE"
    while [ ! -f "$JM_TEST_RELEASE_FILE" ]; do sleep 1; done
fi
printf '%s\n' "$$" >"$JM_TEST_OWNER_FILE"
while :; do
    sleep 1 &
    child=$!
    printf '%s\n' "$child" >"$JM_TEST_CHILD_FILE"
    wait "$child" || true
    child=
    rm -f "$JM_TEST_CHILD_FILE"
done
SH
    chmod +x "$BIN"
    cat >"$FAKE_BIN_DIR/ss" <<'SH'
#!/bin/sh
# 测试用 ss：只报告测试进程占用 CP 配置的两个端口。
[ -f "${JM_TEST_OWNER_FILE:-}" ] || exit 0
owner=$(awk 'NR == 1 { print; exit }' "$JM_TEST_OWNER_FILE")
case "$owner" in ''|*[!0-9]*) exit 0 ;; esac
printf 'LISTEN 0 128 0.0.0.0:18080 0.0.0.0:* users:(("jianmanager-cp",pid=%s,fd=3))\n' "$owner"
printf 'LISTEN 0 128 0.0.0.0:19100 0.0.0.0:* users:(("jianmanager-cp",pid=%s,fd=4))\n' "$owner"
SH
    chmod +x "$FAKE_BIN_DIR/ss"
}

prepare_files

# 仓库原先没有受控 start.sh/stop.sh，模板只通过统一入口暴露 start/stop。
assert_not_file "$ROOT_DIR/scripts/start.sh"
assert_not_file "$ROOT_DIR/scripts/stop.sh"
sh -n "$SCRIPT"
if grep -E '^[[:space:]]*[^#].*pkill[[:space:]]+-f' "$SCRIPT" >/dev/null; then
    fail "生命周期脚本禁止使用按命令行匹配的批量杀进程命令"
fi

# 配置端口必须是正整数且在范围内，端口冲突不得启动新进程。
cp "$CONFIG" "$TEST_ROOT/invalid.yml"
printf '%s\n' 'server:' '  port: 0' 'grpc:' '  port: 19100' >"$TEST_ROOT/invalid.yml"
if TEST_CONFIG="$TEST_ROOT/invalid.yml" run_script start >"$TEST_ROOT/invalid.out" 2>&1; then
    fail "无效端口配置本应失败"
fi
printf '%s\n' "$$" >"$OWNER_FILE"
assert_command_fails run_script start
rm -f "$OWNER_FILE"

# 正常启动会写入正整数 PID、starttime、exe、cwd、cmdline 关联字段，并且重复 start 不会复制进程。
run_script start >"$TEST_ROOT/start.out"
assert_file "$PID_FILE"
assert_contains "$PID_FILE" 'pid='
assert_contains "$PID_FILE" 'starttime='
assert_contains "$PID_FILE" 'exe='
assert_contains "$PID_FILE" "cwd=$INSTALL_DIR"
assert_contains "$PID_FILE" "binary=$BIN"
assert_contains "$PID_FILE" "config=$CONFIG"
pid_one=$(awk -F= '$1 == "pid" { print $2; exit }' "$PID_FILE")
case "$pid_one" in ''|*[!0-9]*) fail "PID 不是正整数" ;; esac
[ "$pid_one" -gt 0 ] || fail "PID 不是正整数"
run_script start >"$TEST_ROOT/start-again.out"
pid_two=$(awk -F= '$1 == "pid" { print $2; exit }' "$PID_FILE")
assert_eq "$pid_one" "$pid_two"

# 端口归属在停止前也必须属于受控 PID；伪造外部占用时拒绝处置。
printf '%s\n' "$$" >"$OWNER_FILE"
assert_command_fails run_script stop
kill -0 "$pid_one" 2>/dev/null || fail "端口冲突不应杀死受控进程"
printf '%s\n' "$pid_one" >"$OWNER_FILE"
kill -KILL "$pid_one" 2>/dev/null || true
rm -f "$PID_FILE" "$OWNER_FILE"

# PID 字段不是正整数时必须拒绝，且保留现场供人工处理。
cat >"$PID_FILE" <<EOF
pid=0x12
starttime=1
exe=$EXPECTED_EXE
cwd=$INSTALL_DIR
binary=$BIN
config=$CONFIG
http_port=18080
grpc_port=19100
EOF
assert_command_fails run_script stop
assert_file "$PID_FILE"
rm -f "$PID_FILE"

# 进程已经退出时只清理 stale PID，不向复用前的数字 PID 发送信号。
cat >"$PID_FILE" <<EOF
pid=999999
starttime=1
exe=$EXPECTED_EXE
cwd=$INSTALL_DIR
binary=$BIN
config=$CONFIG
http_port=18080
grpc_port=19100
EOF
run_script stop >"$TEST_ROOT/stale.out"
assert_not_file "$PID_FILE"

# PID 被其他进程复用时，身份不匹配必须失败且不得误杀复用进程。
/bin/sleep 60 &
foreign_pid=$!
foreign_starttime=$(awk '{ sub(/^.*\) /, ""); print $20 }' "/proc/$foreign_pid/stat")
cat >"$PID_FILE" <<EOF
pid=$foreign_pid
starttime=$foreign_starttime
exe=$EXPECTED_EXE
cwd=$INSTALL_DIR
binary=$BIN
config=$CONFIG
http_port=18080
grpc_port=19100
EOF
assert_command_fails run_script stop
kill -0 "$foreign_pid" 2>/dev/null || fail "PID reuse 分支误杀了无关进程"
kill "$foreign_pid" 2>/dev/null || true
rm -f "$PID_FILE"

# 并发 start/stop 共用 mkdir 原子锁；已有锁时任何生命周期操作都必须拒绝。
mkdir "$LOCK_DIR"
assert_command_fails run_script start
assert_command_fails run_script stop
rmdir "$LOCK_DIR"

# 二次身份校验与 KILL 门禁必须同时存在；源码契约断言避免测试夹具主动杀进程造成环境差异。
assert_contains "$SCRIPT" 'TERM 等待期间进程身份变化，拒绝 KILL'
assert_contains "$SCRIPT" 'TERM 超时后二次身份校验失败，拒绝 KILL'
assert_contains "$SCRIPT" 'kill -KILL "$PID"'
assert_contains "$SCRIPT" 'process_state=$(proc_state "$PID")'
term_line=$(awk '/kill -TERM "\$PID"/ { print NR; exit }' "$SCRIPT")
recheck_line=$(awk '/TERM 超时后二次身份校验失败/ { print NR; exit }' "$SCRIPT")
kill_line=$(awk '/kill -KILL "\$PID"/ { print NR; exit }' "$SCRIPT")
[ "$term_line" -lt "$recheck_line" ] || fail "TERM 后缺少超时二次身份校验"
[ "$recheck_line" -lt "$kill_line" ] || fail "KILL 排在二次身份校验之前"

# 检查 stale / reuse / 二次复核 / 锁等关键分支均已走过。
echo 'CP 生命周期脚本契约测试通过'
