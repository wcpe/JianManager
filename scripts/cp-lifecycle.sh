#!/bin/sh
# Control Plane 受控生命周期模板：仅按 PID 文件与 /proc 多特征身份处置目标进程。
#
# 用法：
#   JM_CP_INSTALL_DIR=/opt/jianmanager-cp scripts/cp-lifecycle.sh start
#   JM_CP_INSTALL_DIR=/opt/jianmanager-cp scripts/cp-lifecycle.sh stop
#
# 目标机要求：Linux /proc、POSIX sh、awk、readlink，以及 ss 或 netstat。
# 脚本只向单个已复核 PID 发送 TERM/KILL，不使用按命令行模糊匹配的批量杀进程命令。
set -eu

ACTION=${1:-}
case "$ACTION" in
    start|stop) ;;
    *)
        echo "用法: $0 start|stop" >&2
        exit 2
        ;;
esac

INSTALL_DIR=${JM_CP_INSTALL_DIR:-/opt/jianmanager-cp}
BINARY=${JM_CP_BINARY:-$INSTALL_DIR/current/jianmanager-cp}
CONFIG=${JM_CP_CONFIG:-$INSTALL_DIR/control-plane.yml}
PID_FILE=${JM_CP_PID_FILE:-$INSTALL_DIR/run/control-plane.pid}
LOG_FILE=${JM_CP_LOG_FILE:-$INSTALL_DIR/logs/control-plane.log}
LOCK_DIR=${JM_CP_LOCK_DIR:-$INSTALL_DIR/run/control-plane.lock}
START_TIMEOUT=${JM_CP_START_TIMEOUT:-30}
STOP_TIMEOUT=${JM_CP_STOP_TIMEOUT:-30}
POLL_INTERVAL=${JM_CP_POLL_INTERVAL:-1}
EXPECTED_EXE=${JM_CP_EXPECTED_EXE:-}

fail() {
    echo "错误: $*" >&2
    exit 1
}

is_positive_integer() {
    case "${1:-}" in
        ''|*[!0-9]*) return 1 ;;
    esac
    [ "$1" -gt 0 ] 2>/dev/null
}

is_nonnegative_integer() {
    case "${1:-}" in
        ''|*[!0-9]*) return 1 ;;
    esac
    [ "$1" -ge 0 ] 2>/dev/null
}

is_port() {
    is_positive_integer "$1" || return 1
    [ "$1" -le 65535 ]
}

validate_runtime_options() {
    is_positive_integer "$START_TIMEOUT" || fail "JM_CP_START_TIMEOUT 必须是正整数"
    is_nonnegative_integer "$STOP_TIMEOUT" || fail "JM_CP_STOP_TIMEOUT 必须是非负整数"
    is_positive_integer "$POLL_INTERVAL" || fail "JM_CP_POLL_INTERVAL 必须是正整数"
    [ -d /proc ] || fail "目标机缺少 /proc，拒绝执行生命周期操作"
}

absolute_path() {
    path=$1
    case "$path" in
        /*) printf '%s\n' "$path" ;;
        *) printf '%s/%s\n' "$(pwd)" "$path" ;;
    esac
}

canonical_existing_path() {
    path=$1
    if command -v readlink >/dev/null 2>&1 && readlink -f "$path" 2>/dev/null; then
        return 0
    fi
    dir=$(CDPATH= cd -- "$(dirname -- "$path")" && pwd)
    printf '%s/%s\n' "$dir" "$(basename -- "$path")"
}

field() {
    key=$1
    file=$2
    awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$file"
}

proc_starttime() {
    pid=$1
    awk '{ sub(/^.*\\) /, ""); print $20 }' "/proc/$pid/stat" 2>/dev/null
}

proc_state() {
    pid=$1
    awk '{ sub(/^.*\\) /, ""); print $1 }' "/proc/$pid/stat" 2>/dev/null
}

proc_link() {
    pid=$1
    name=$2
    readlink "/proc/$pid/$name" 2>/dev/null
}

proc_cmdline() {
    pid=$1
    tr '\000' ' ' <"/proc/$pid/cmdline" 2>/dev/null
}

read_config_port() {
    section=$1
    awk -v wanted="$section" '
        /^[[:space:]]*#/ { next }
        /^[^[:space:]][^:]*:[[:space:]]*$/ {
            current=$0
            sub(/:.*/, "", current)
            next
        }
        current == wanted && /^[[:space:]]+port:[[:space:]]*[0-9]+([[:space:]]*#.*)?$/ {
            value=$0
            sub(/^[^:]*:[[:space:]]*/, "", value)
            sub(/[[:space:]]+#.*/, "", value)
            print value
            exit
        }
    ' "$CONFIG"
}

validate_config() {
    [ -f "$CONFIG" ] || fail "配置文件不存在: $CONFIG"
    [ ! -L "$CONFIG" ] || fail "配置文件不得是符号链接: $CONFIG"
    HTTP_PORT=$(read_config_port server)
    GRPC_PORT=$(read_config_port grpc)
    is_port "$HTTP_PORT" || fail "配置中的 server.port 必须是 1..65535 的正整数"
    is_port "$GRPC_PORT" || fail "配置中的 grpc.port 必须是 1..65535 的正整数"
    [ "$HTTP_PORT" != "$GRPC_PORT" ] || fail "server.port 与 grpc.port 不得相同"
    CONFIG=$(canonical_existing_path "$CONFIG")
}

select_port_tool() {
    if command -v ss >/dev/null 2>&1; then
        PORT_TOOL=ss
    elif command -v netstat >/dev/null 2>&1; then
        PORT_TOOL=netstat
    else
        fail "缺少 ss 或 netstat，无法校验端口归属"
    fi
}

port_owner_pids() {
    port=$1
    if [ "$PORT_TOOL" = ss ]; then
        ss -H -ltnp 2>/dev/null | awk -v port="$port" '
            $4 ~ (":" port "$|\\]" port "$") {
                if (match($0, /pid=[0-9]+/)) print substr($0, RSTART + 4, RLENGTH - 4)
            }
        ' | awk '!seen[$0]++'
    else
        netstat -ltnp 2>/dev/null | awk -v port="$port" '
            $4 ~ (":" port "$|\\]" port "$") {
                if (match($0, /[0-9]+\\/[^ ]+$/)) {
                    value=substr($0, RSTART, RLENGTH)
                    sub(/\\/.*/, "", value)
                    print value
                }
            }
        ' | awk '!seen[$0]++'
    fi
}

ports_owned_by() {
    wanted_pid=$1
    for port in "$HTTP_PORT" "$GRPC_PORT"; do
        owners=$(port_owner_pids "$port")
        [ -n "$owners" ] || return 1
        found=0
        for owner in $owners; do
            [ "$owner" = "$wanted_pid" ] && found=1
        done
        [ "$found" = 1 ] || return 1
    done
    return 0
}

validate_port_ownership() {
    target_pid=${1:-}
    for port in "$HTTP_PORT" "$GRPC_PORT"; do
        owners=$(port_owner_pids "$port")
        for owner in $owners; do
            [ -n "$target_pid" ] && [ "$owner" = "$target_pid" ] && continue
            fail "端口 $port 已被其他 PID $owner 占用，拒绝处置"
        done
    done
}

record_identity_matches_process() {
    pid=$1
    record_starttime=$2
    record_exe=$3
    record_cwd=$4
    record_binary=$5
    record_config=$6
    [ -d "/proc/$pid" ] || return 1
    [ "$(proc_state "$pid")" != Z ] || return 1
    current_starttime=$(proc_starttime "$pid")
    current_exe=$(proc_link "$pid" exe)
    current_cwd=$(proc_link "$pid" cwd)
    current_cmdline=$(proc_cmdline "$pid")
    [ -n "$current_starttime" ] && [ "$current_starttime" = "$record_starttime" ] || return 1
    [ -n "$current_exe" ] && [ "$current_exe" = "$record_exe" ] || return 1
    [ -n "$current_cwd" ] && [ "$current_cwd" = "$record_cwd" ] || return 1
    record_binary_name=$(basename -- "$record_binary")
    record_config_name=$(basename -- "$record_config")
    case "$current_cmdline" in
        *"$record_binary"*|*"$record_binary_name"*) ;;
        *) return 1 ;;
    esac
    case "$current_cmdline" in
        *"$record_config"*|*"$record_config_name"*) ;;
        *) return 1 ;;
    esac
    if [ -n "$EXPECTED_EXE" ] && [ "$current_exe" != "$EXPECTED_EXE" ]; then
        return 1
    fi
    return 0
}

load_pid_record() {
    [ -f "$PID_FILE" ] || return 1
    first_line=$(sed -n '1p' "$PID_FILE")
    case "$first_line" in
        pid=*)
            PID=$(field pid "$PID_FILE")
            RECORD_STARTTIME=$(field starttime "$PID_FILE")
            RECORD_EXE=$(field exe "$PID_FILE")
            RECORD_CWD=$(field cwd "$PID_FILE")
            RECORD_BINARY=$(field binary "$PID_FILE")
            RECORD_CONFIG=$(field config "$PID_FILE")
            RECORD_HTTP_PORT=$(field http_port "$PID_FILE")
            RECORD_GRPC_PORT=$(field grpc_port "$PID_FILE")
            ;;
        *[!0-9]*|'')
            fail "PID 文件格式无效，拒绝处置: $PID_FILE"
            ;;
        *)
            # 兼容旧版只写数字 PID 的文件：仅当进程身份和两个端口均匹配时迁移。
            PID=$first_line
            if [ ! -d "/proc/$PID" ] || ! kill -0 "$PID" 2>/dev/null; then
                RECORD_STARTTIME=1
                RECORD_EXE="$BINARY"
                RECORD_CWD="$INSTALL_DIR"
                RECORD_BINARY="$BINARY"
                RECORD_CONFIG="$CONFIG"
                RECORD_HTTP_PORT="$HTTP_PORT"
                RECORD_GRPC_PORT="$GRPC_PORT"
                return 0
            fi
            legacy_starttime=$(proc_starttime "$PID")
            legacy_exe=$(proc_link "$PID" exe)
            legacy_cwd=$(proc_link "$PID" cwd)
            record_identity_matches_process "$PID" "$legacy_starttime" "$legacy_exe" "$legacy_cwd" "$BINARY" "$CONFIG" || fail "旧版 PID 文件身份无法核验，拒绝处置: $PID_FILE"
            ports_owned_by "$PID" || fail "旧版 PID 文件端口归属无法核验，拒绝处置: $PID_FILE"
            RECORD_STARTTIME=$legacy_starttime
            RECORD_EXE=$legacy_exe
            RECORD_CWD=$legacy_cwd
            RECORD_BINARY="$BINARY"
            RECORD_CONFIG="$CONFIG"
            RECORD_HTTP_PORT="$HTTP_PORT"
            RECORD_GRPC_PORT="$GRPC_PORT"
            write_pid_record "$PID" "$RECORD_STARTTIME" "$RECORD_EXE" "$RECORD_CWD"
            ;;
    esac
    is_positive_integer "$PID" || fail "PID 文件中的 pid 不是正整数: $PID_FILE"
    is_positive_integer "$RECORD_STARTTIME" || fail "PID 文件中的 starttime 不是正整数: $PID_FILE"
    [ -n "$RECORD_EXE" ] || fail "PID 文件缺少 exe: $PID_FILE"
    [ -n "$RECORD_CWD" ] || fail "PID 文件缺少 cwd: $PID_FILE"
    [ -n "$RECORD_BINARY" ] || fail "PID 文件缺少 binary: $PID_FILE"
    [ -n "$RECORD_CONFIG" ] || fail "PID 文件缺少 config: $PID_FILE"
    [ "$RECORD_BINARY" = "$BINARY" ] || fail "PID 文件中的 binary 与当前配置不一致，拒绝处置"
    [ "$RECORD_CONFIG" = "$CONFIG" ] || fail "PID 文件中的 config 与当前配置不一致，拒绝处置"
    [ "$RECORD_CWD" = "$INSTALL_DIR" ] || fail "PID 文件中的 cwd 与安装根不一致，拒绝处置"
    is_port "$RECORD_HTTP_PORT" || fail "PID 文件中的 http_port 无效: $PID_FILE"
    is_port "$RECORD_GRPC_PORT" || fail "PID 文件中的 grpc_port 无效: $PID_FILE"
    [ "$RECORD_HTTP_PORT" = "$HTTP_PORT" ] || fail "PID 文件端口与当前 server.port 不一致，拒绝处置"
    [ "$RECORD_GRPC_PORT" = "$GRPC_PORT" ] || fail "PID 文件端口与当前 grpc.port 不一致，拒绝处置"
}

pid_state() {
    if ! load_pid_record; then
        PID_STATE=missing
        return 0
    fi
    if [ ! -d "/proc/$PID" ] || ! kill -0 "$PID" 2>/dev/null; then
        PID_STATE=stale
    elif record_identity_matches_process "$PID" "$RECORD_STARTTIME" "$RECORD_EXE" "$RECORD_CWD" "$RECORD_BINARY" "$RECORD_CONFIG"; then
        PID_STATE=owned
    else
        PID_STATE=reused
    fi
}

write_pid_record() {
    pid=$1
    starttime=$2
    exe=$3
    cwd=$4
    tmp="$PID_FILE.tmp.$$"
    umask 077
    {
        printf 'pid=%s\n' "$pid"
        printf 'starttime=%s\n' "$starttime"
        printf 'exe=%s\n' "$exe"
        printf 'cwd=%s\n' "$cwd"
        printf 'binary=%s\n' "$BINARY"
        printf 'config=%s\n' "$CONFIG"
        printf 'http_port=%s\n' "$HTTP_PORT"
        printf 'grpc_port=%s\n' "$GRPC_PORT"
    } >"$tmp"
    mv -f "$tmp" "$PID_FILE"
}

release_lock() {
    if [ "${LOCK_HELD:-0}" = 1 ]; then
        rmdir "$LOCK_DIR" 2>/dev/null || true
        LOCK_HELD=0
    fi
}

acquire_lock() {
    mkdir -p "$(dirname -- "$LOCK_DIR")"
    if ! mkdir "$LOCK_DIR" 2>/dev/null; then
        fail "生命周期操作正在进行（锁: $LOCK_DIR）"
    fi
    LOCK_HELD=1
    trap release_lock 0 HUP INT TERM
}

wait_for_start() {
    pid=$1
    elapsed=0
    while [ "$elapsed" -lt "$START_TIMEOUT" ]; do
        if [ ! -d "/proc/$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
            return 1
        fi
        starttime=$(proc_starttime "$pid")
        exe=$(proc_link "$pid" exe)
        cwd=$(proc_link "$pid" cwd)
        if is_positive_integer "$starttime" && [ -n "$exe" ] && [ "$cwd" = "$INSTALL_DIR" ]; then
            record_identity_matches_process "$pid" "$starttime" "$exe" "$cwd" "$BINARY" "$CONFIG" || :
            if record_identity_matches_process "$pid" "$starttime" "$exe" "$cwd" "$BINARY" "$CONFIG" && ports_owned_by "$pid"; then
                write_pid_record "$pid" "$starttime" "$exe" "$cwd"
                return 0
            fi
        fi
        sleep "$POLL_INTERVAL"
        elapsed=$((elapsed + POLL_INTERVAL))
    done
    return 1
}

start_cp() {
    validate_config
    [ -f "$BINARY" ] || fail "Control Plane 二进制不存在: $BINARY"
    [ -x "$BINARY" ] || fail "Control Plane 二进制不可执行: $BINARY"
    INSTALL_DIR=$(canonical_existing_path "$INSTALL_DIR")
    BINARY=$(absolute_path "$BINARY")
    EXPECTED_EXE=${EXPECTED_EXE:-$(canonical_existing_path "$BINARY")}
    pid_state
    state=$PID_STATE
    case "$state" in
        owned)
            validate_port_ownership "$PID"
            echo "Control Plane 已运行，PID=$PID"
            return 0
            ;;
        stale)
            rm -f "$PID_FILE"
            echo "已清理过期 PID 文件: $PID_FILE"
            ;;
        reused)
            fail "PID $PID 已被其他进程复用，拒绝启动并保留 PID 文件"
            ;;
        missing) ;;
    esac
    validate_port_ownership
    mkdir -p "$(dirname -- "$PID_FILE")" "$(dirname -- "$LOG_FILE")"
    echo "启动 Control Plane: $BINARY $CONFIG"
    (
        cd "$INSTALL_DIR"
        nohup "$BINARY" "$CONFIG" >>"$LOG_FILE" 2>&1 </dev/null &
        printf '%s\n' "$!"
    ) >"$PID_FILE.launch.$$"
    pid=$(cat "$PID_FILE.launch.$$" )
    rm -f "$PID_FILE.launch.$$"
    is_positive_integer "$pid" || fail "无法取得 Control Plane PID"
    if ! wait_for_start "$pid"; then
        kill -0 "$pid" 2>/dev/null && kill -TERM "$pid" 2>/dev/null || true
        fail "Control Plane 启动校验失败（身份或端口未就绪）"
    fi
    echo "Control Plane 已启动，PID=$pid，HTTP=$HTTP_PORT，gRPC=$GRPC_PORT"
}

stop_cp() {
    validate_config
    INSTALL_DIR=$(canonical_existing_path "$INSTALL_DIR")
    BINARY=$(absolute_path "$BINARY")
    EXPECTED_EXE=${EXPECTED_EXE:-$(canonical_existing_path "$BINARY")}
    pid_state
    state=$PID_STATE
    case "$state" in
        missing)
            echo "Control Plane 未运行（无 PID 文件）"
            return 0
            ;;
        stale)
            rm -f "$PID_FILE"
            echo "已清理过期 PID 文件: $PID_FILE"
            return 0
            ;;
        reused)
            fail "PID $PID 已被其他进程复用，拒绝发送信号"
            ;;
        owned) ;;
    esac
    validate_port_ownership "$PID"
    echo "发送 TERM 到 Control Plane PID=$PID"
    kill -TERM "$PID" 2>/dev/null || fail "无法向 PID $PID 发送 TERM"
    elapsed=0
    while [ "$elapsed" -lt "$STOP_TIMEOUT" ]; do
        process_state=$(proc_state "$PID")
        if [ ! -d "/proc/$PID" ] || ! kill -0 "$PID" 2>/dev/null || [ -z "$process_state" ] || [ "$process_state" = Z ]; then
            rm -f "$PID_FILE"
            echo "Control Plane 已停止"
            return 0
        fi
        if ! record_identity_matches_process "$PID" "$RECORD_STARTTIME" "$RECORD_EXE" "$RECORD_CWD" "$RECORD_BINARY" "$RECORD_CONFIG"; then
            fail "TERM 等待期间进程身份变化，拒绝 KILL"
        fi
        sleep "$POLL_INTERVAL"
        elapsed=$((elapsed + POLL_INTERVAL))
    done
    # 超时后必须再次完整复核，防止 PID 在等待期间被复用或命令行/工作目录变化。
    if ! record_identity_matches_process "$PID" "$RECORD_STARTTIME" "$RECORD_EXE" "$RECORD_CWD" "$RECORD_BINARY" "$RECORD_CONFIG"; then
        fail "TERM 超时后二次身份校验失败，拒绝 KILL"
    fi
    validate_port_ownership "$PID"
    echo "TERM 超时，二次校验通过后发送 KILL 到 PID=$PID"
    kill -KILL "$PID" 2>/dev/null || fail "无法向 PID $PID 发送 KILL"
    elapsed=0
    while [ "$elapsed" -lt "$STOP_TIMEOUT" ]; do
        process_state=$(proc_state "$PID")
        if [ ! -d "/proc/$PID" ] || ! kill -0 "$PID" 2>/dev/null || [ -z "$process_state" ] || [ "$process_state" = Z ]; then
            rm -f "$PID_FILE"
            echo "Control Plane 已强制停止"
            return 0
        fi
        sleep "$POLL_INTERVAL"
        elapsed=$((elapsed + POLL_INTERVAL))
    done
    if [ ! -d "/proc/$PID" ] || ! kill -0 "$PID" 2>/dev/null || [ "$(proc_state "$PID")" = Z ]; then
        rm -f "$PID_FILE"
        echo "Control Plane 已强制停止"
        return 0
    fi
    fail "KILL 后 PID $PID 仍存活，保留 PID 文件供人工处理"
}

validate_runtime_options
acquire_lock
select_port_tool
case "$ACTION" in
    start) start_cp ;;
    stop) stop_cp ;;
esac
