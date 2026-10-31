#!/bin/sh
#  采集索引回滚脚本（spec §2.3）：把采集索引从 SQLite 退回旧 ingest.state.json。
#
# 步骤：停止 Worker → 归档现有 ingest.index.db → 把归档的旧 JSON 改回 ingest.state.json → 启动 Worker。
# 因两种格式共用同一语义层（游标/缺口/投影/实例绑定），回滚**不需要反向导出**：
# 旧 JSON 里已经是迁移前的完整状态，Worker 下次启动会重新读它。
#
# 本脚本只做「移动/改名」，从不删除任何文件：索引库改名成 .rolled-back.<时间戳>，
# 旧 JSON 从 .migrated 归档改回原路径。任何一步不满足前置条件即中止，绝不覆盖既有数据。
#
# 用法：
#   JM_DATA_DIR=<数据根> scripts/rollback-log-index.sh            # 执行回滚
#   JM_DATA_DIR=<数据根> scripts/rollback-log-index.sh --dry-run  # 只看会做什么
#   JM_DATA_DIR=<数据根> scripts/rollback-log-index.sh --export <文件>  # 回滚前先只读导出索引为 JSON
#
#
# 环境变量：
#   JM_DATA_DIR          Worker 数据根（默认取 JIANMANAGER_DATA_DIR，再退到 <安装根>/data）
#   JM_INSTALL_DIR       Worker 安装根（配合 JM_DATA_DIR 缺省推导；默认 /opt/jianmanager，
#                        与 install-worker.sh:INSTALL_DIR、deploy-worker.sh:DEF_INSTALL 同口径）
#   JM_WORKER_SERVICE    systemd 用户级单元名；置空则改用 PID 文件方式停/启
#   JM_WORKER_PID_FILE   PID 文件（PID 文件方式使用；默认 <数据根的父目录>/logs/worker.pid）
#   JM_WORKER_START_CMD 自定义启动命令（PID 文件方式使用）
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

# 数据根口径与 install-worker.sh / deploy-worker.sh 一致：<安装根>/data。
# 不要在此另立约定（历史上的 /opt/jianmanager-worker 在全仓零出现，只会让忘了传
# JM_DATA_DIR 的运维撞上「数据根不存在」，或在旧残留目录上回滚错对象）。
INSTALL_ROOT=${JM_INSTALL_DIR:-/opt/jianmanager}
DATA_DIR=${JM_DATA_DIR:-${JIANMANAGER_DATA_DIR:-$INSTALL_ROOT/data}}
LOG_DIR="$DATA_DIR/var/log"
INDEX_DB="$LOG_DIR/ingest.index.db"
LEGACY_JSON="$LOG_DIR/ingest.state.json"
# PID 文件路径口径：由 Worker 自己决定，**与 `apps/worker/pid_record.go` 的
# `writeWorkerPIDRecord(root.Base(), …)` 对齐**——它写的是 `<dir(Base)>/logs/worker.pid`。
#
# 此处原先默认 `$DATA_DIR/run/worker.pid`，与真实写入位置不一致 ⇒ PID 方式下必然
# `缺少 PID 文件` 直接中止（安全方向，但该路径功能上不可用）。真机实测文件内容形如
# `data_dir=/tmp/jmrk/data` 而文件位于 `/tmp/jmrk/logs/worker.pid`，即数据根的**父目录**下。
PID_FILE=${JM_WORKER_PID_FILE:-$(dirname -- "$DATA_DIR")/logs/worker.pid}
WORKER_SERVICE=${JM_WORKER_SERVICE-jianmanager-worker}
WORKER_START_CMD=${JM_WORKER_START_CMD:-}

ACTION=rollback
EXPORT_FILE=""
while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run) ACTION=dry-run ;;
        --export)
            [ $# -ge 2 ] || { echo "错误: --export 需要文件路径" >&2; exit 2; }
            ACTION=export
            EXPORT_FILE=$2
            shift
            ;;
        -h|--help)
            # 打印文件头的说明段：从第 2 行到脚本正文起点 `set -eu` **之前**。
            # 按标记取而不是硬编码行号，注释增删不会把输出截断在半句。
            awk 'NR == 1 { next } /^set -eu/ { exit } { print }' "$0"
            exit 0
            ;;
        *) echo "错误: 未知参数 $1" >&2; exit 2 ;;
    esac
    shift
done

die() { echo "错误: $*" >&2; exit 1; }
say() { echo "[rollback-log-index] $*"; }

# ── PID 文件读取与进程身份核验 ────────────────────────────────────────────────
# 与 scripts/cp-lifecycle.sh 的 field/proc_starttime/proc_link **同口径**（apps/worker/pid_record.go
# 的注释也明确声明同口径），故此处复用同一实现而非另造。
field() {
    key=$1
    file=$2
    awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$file"
}

proc_starttime() {
    pid=$1
    awk '{ sub(/^.*\) /, ""); print $20 }' "/proc/$pid/stat" 2>/dev/null
}

proc_link() {
    pid=$1
    name=$2
    readlink "/proc/$pid/$name" 2>/dev/null
}

# worker_pid_identity_matches 核验「该 PID 现在仍是这份记录描述的那个进程」。
#
# **为什么必须有它**：只凭 `kill -0 "$pid"` 判活无法区分「Worker 仍在跑」与「PID 已被系统复用」——
# 而本脚本在判定「已停止」后会**立即移动索引库与旧 JSON**。若此时真正的 Worker 仍在写
# `ingest.index.db`，`mv`（同文件系统上是 rename）后它继续写被改名的文件，新启动的 Worker
# 拿到空索引库 + 旧 JSON ⇒ **采集账本静默回退到迁移前状态**，下线窗口内写入的源位置区间丢失。
# pid_record.go 自己就记录过这个坑（2026-09-28 生产：worker.pid 停在多日前的 PID，
# 内容指向早已退出的进程），故它在文件里写了 starttime/exe/cwd 三个纯 PID 做不到的字段。
# 身份不符一律**硬失败**，交人工判断（与 cp-lifecycle.sh 同策略）。
worker_pid_identity_matches() {
    pid=$1
    rec_starttime=$2
    rec_exe=$3
    rec_cwd=$4
    # /proc 不可用（非 Linux）：无字段可核验，退化为只认 PID 存在性——此时不阻断，
    # 但明确告知核验被跳过，不假装已核验。
    if [ ! -d "/proc/$pid" ]; then
        say "警告：/proc/$pid 不可用，跳过进程身份核验（仅按 PID 判活）"
        return 0
    fi
    live_starttime=$(proc_starttime "$pid")
    # starttime 是「进程启动时刻」的单调刻度，PID 复用时必然不同 ⇒ 这一项即可决定性区分。
    if [ -n "$rec_starttime" ] && [ -n "$live_starttime" ] && [ "$rec_starttime" != "$live_starttime" ]; then
        return 1
    fi
    live_exe=$(proc_link "$pid" exe)
    if [ -n "$rec_exe" ] && [ -n "$live_exe" ] && [ "$rec_exe" != "$live_exe" ]; then
        return 1
    fi
    live_cwd=$(proc_link "$pid" cwd)
    if [ -n "$rec_cwd" ] && [ -n "$live_cwd" ] && [ "$rec_cwd" != "$live_cwd" ]; then
        return 1
    fi
    return 0
}

[ -d "$DATA_DIR" ] || die "数据根不存在: $DATA_DIR"

# 归档名带时间戳，多次回滚不会互相覆盖（保留全部历史版本）。
stamp() { date -u +%Y%m%dT%H%M%SZ; }

find_archived_legacy() {
    # 迁移归档：ingest.state.json.migrated（首个版本）或 .migrated.<时间戳>（被占用时）。
    for candidate in "$LEGACY_JSON.migrated" "$LEGACY_JSON".migrated.*; do
        if [ -f "$candidate" ]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    return 1
}

worker_stop() {
    if [ -n "$WORKER_SERVICE" ] && command -v systemctl >/dev/null 2>&1; then
        say "停止 Worker（systemd --user $WORKER_SERVICE）"
        systemctl --user stop "$WORKER_SERVICE" || die "停止 $WORKER_SERVICE 失败"
        return 0
    fi
    [ -f "$PID_FILE" ] || die "缺少 PID 文件 $PID_FILE（无法确认 Worker 已停止；请先停 Worker 或用 JM_WORKER_SERVICE 指定单元）"
    # PID 文件由 Worker 写成 `key=value` 多行记录（pid/starttime/exe/cwd/…）；兼容旧版仅一行裸 PID。
    first_line=$(sed -n '1p' "$PID_FILE")
    case "$first_line" in
        pid=*)
            pid=$(field pid "$PID_FILE")
            rec_starttime=$(field starttime "$PID_FILE")
            rec_exe=$(field exe "$PID_FILE")
            rec_cwd=$(field cwd "$PID_FILE")
            ;;
        ''|*[!0-9]*) die "PID 文件 $PID_FILE 格式不可解析（既非 pid= 记录也非裸 PID）" ;;
        *)
            # 旧版裸 PID：无身份字段可核验，按原名读取并明确告知核验被跳过。
            pid=$first_line
            rec_starttime=""
            rec_exe=""
            rec_cwd=""
            say "警告：PID 文件为旧版裸 PID 格式，缺少 starttime/exe/cwd，无法核验进程身份"
            ;;
    esac
    case "$pid" in
        ''|*[!0-9]*) die "PID 文件 $PID_FILE 中的 pid 不可解析" ;;
    esac
    if kill -0 "$pid" 2>/dev/null; then
        # 判活之外**必须**核验身份：PID 复用时 kill -0 为真但那是别的进程，而对它发 TERM
        # 既停不了 Worker（随后 mv 索引库将对着仍在写的 Worker 生效），又会误杀无关进程。
        worker_pid_identity_matches "$pid" "$rec_starttime" "$rec_exe" "$rec_cwd" \
            || die "PID 文件 $PID_FILE 描述的身份与 /proc/$pid 不符（PID 可能已被复用）；拒绝处置以防移走仍在使用的索引库，请人工确认后重试"
        say "停止 Worker（PID $pid）"
        kill -TERM "$pid" || die "无法向 PID $pid 发送 TERM"
        waited=0
        while kill -0 "$pid" 2>/dev/null; do
            waited=$((waited + 1))
            [ "$waited" -le 30 ] || die "Worker（PID $pid）未在 30 秒内退出；请人工处理后重试"
            sleep 1
        done
    else
        say "Worker 进程（PID $pid）已不存在，继续"
    fi
}

worker_start() {
    if [ -n "$WORKER_SERVICE" ] && command -v systemctl >/dev/null 2>&1; then
        say "启动 Worker（systemd --user $WORKER_SERVICE）"
        systemctl --user start "$WORKER_SERVICE" || die "启动 $WORKER_SERVICE 失败"
        return 0
    fi
    if [ -n "$WORKER_START_CMD" ]; then
        say "启动 Worker（自定义命令）"
        # shellcheck disable=SC2086
        sh -c "$WORKER_START_CMD" || die "自定义启动命令失败"
        return 0
    fi
    say "未配置 systemd 单元或启动命令：请手工启动 Worker（采集将按旧 JSON 继续）"
}

case "$ACTION" in
export)
    [ -n "$EXPORT_FILE" ] || die "--export 需要文件路径"
    [ -f "$INDEX_DB" ] || die "索引库不存在: $INDEX_DB"
    # 只读导出：借 worker 子命令（不启动采集、不写索引库）。
    worker_bin=${JM_WORKER_BIN:-}
    if [ -z "$worker_bin" ]; then
        for candidate in "$DATA_DIR/worker" /opt/jianmanager/worker "$(command -v jianmanager-worker 2>/dev/null || true)"; do
            if [ -n "$candidate" ] && [ -x "$candidate" ]; then worker_bin=$candidate; break; fi
        done
    fi
    if [ -n "$worker_bin" ]; then
        "$worker_bin" log-index-export --data-dir "$DATA_DIR" --out "$EXPORT_FILE" \
            || die "导出失败（可改用 sqlite3 直接查询 $INDEX_DB）"
        say "已导出: $EXPORT_FILE"
        exit 0
    fi
    if command -v sqlite3 >/dev/null 2>&1; then
        say "未找到 worker 可执行文件，降级用 sqlite3 导出关键表"
        sqlite3 "$INDEX_DB" ".mode insert" "SELECT * FROM gap;" > "$EXPORT_FILE" \
            || die "sqlite3 导出失败"
        say "已导出（仅缺口表）: $EXPORT_FILE"
        exit 0
    fi
    die "既无 worker 可执行文件也无 sqlite3；请用 JM_WORKER_BIN 指定路径"
    ;;
esac

# --- 回滚前的门禁 ---------------------------------------------------------
[ -f "$INDEX_DB" ] || die "索引库不存在: $INDEX_DB（当前可能未迁移，无需回滚）"
archived=$(find_archived_legacy) || die "找不到迁移归档 $LEGACY_JSON.migrated（无法回滚：没有迁移前的旧状态）"
if [ -f "$LEGACY_JSON" ]; then
    # 已存在旧 JSON：不得覆盖（可能是人工放回或上一次回滚中断）。
    die "旧状态文件已存在: $LEGACY_JSON；请人工确认后移走，本脚本不覆盖既有数据"
fi

index_archive="$INDEX_DB.rolled-back.$(stamp)"
say "数据根: $DATA_DIR"
say "索引库: $INDEX_DB"
say "旧状态归档: $archived"
say "索引归档目标: $index_archive"
if [ "$ACTION" = "dry-run" ]; then
    say "dry-run：将执行「停止 Worker → 归档索引库 → 改回旧 JSON → 启动 Worker」，现未做任何改动"
    exit 0
fi

# --- 执行回滚 -------------------------------------------------------------
worker_stop

say "归档索引库（保留，不删除）"
mv "$INDEX_DB" "$index_archive" || die "归档索引库失败"
# WAL 伴生文件随库一起归档：旧格式不读它们，留着会让下次迁移误认为存在未归并内容。
for suffix in -wal -shm; do
    if [ -f "$INDEX_DB$suffix" ]; then
        mv "$INDEX_DB$suffix" "$index_archive$suffix" || die "归档索引伴生文件失败: $INDEX_DB$suffix"
    fi
done

say "把迁移归档的旧 JSON 改回 ingest.state.json"
mv "$archived" "$LEGACY_JSON" || die "恢复旧状态文件失败（索引库已归档在 $index_archive，可人工恢复）"

worker_start

say "回滚完成。校验要点："
say "  1) 旧 JSON 已就位: $LEGACY_JSON"
say "  2) 索引库已归档: $index_archive（确认采集正常后可人工清理）"
say "  3) 回滚前后的账本语义一致（游标/缺口/投影/实例绑定同源），无需反向导出"
