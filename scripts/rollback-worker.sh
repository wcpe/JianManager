#!/bin/sh
# JianManager Worker 用户级 systemd 版本回滚脚本（FR-410）。
# 仅切换 current 并重启已版本化服务；不会删除任何已部署版本、数据、配置或节点身份。
set -eu

BIN_NAME="jianmanager-worker"
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
VERSIONED_LIB="$REPO_ROOT/scripts/lib/versioned-user-layout.sh"

JM_SSH_HOST="${JM_SSH_HOST:-}"
JM_SSH_PORT="${JM_SSH_PORT:-22}"
JM_SSH_USER="${JM_SSH_USER:-root}"
JM_SSH_KEY="${JM_SSH_KEY:-}"
JM_SERVICE_SCOPE="${JM_SERVICE_SCOPE:-user}"
JM_INSTALL_DIR="${JM_INSTALL_DIR:-}"

usage() {
    echo "用法: JM_SSH_HOST=<主机> [JM_*=…] $0 <版本目录名>" >&2
}
die() { echo "错误: $*" >&2; exit 1; }

worker_service_name() {
    dir_base=$(basename "$1")
    service_name="jianmanager-worker"
    if [ "$dir_base" != "jianmanager" ]; then
        suffix=$(printf '%s' "$dir_base" | sed -e 's/^jianmanager-\{0,1\}//' -e 's/[^A-Za-z0-9_-]/-/g')
        [ -n "$suffix" ] && service_name="jianmanager-worker-$suffix"
    fi
    printf '%s\n' "$service_name"
}

[ $# -eq 1 ] || { usage; exit 1; }
RELEASE_NAME=$1
case "$RELEASE_NAME" in
    ""|*[!A-Za-z0-9._+-]*) die "版本目录名仅支持字母、数字、.、_、+、-" ;;
esac
case "$JM_SERVICE_SCOPE" in
    user|auto) : ;;
    system) die "版本化回滚仅支持 JM_SERVICE_SCOPE=user；system 档保持既有直装/.bak 行为" ;;
    *) die "JM_SERVICE_SCOPE 仅支持 user|auto（收到: $JM_SERVICE_SCOPE）" ;;
esac
[ -n "$JM_SSH_HOST" ] || die "缺少 JM_SSH_HOST"
[ -f "$VERSIONED_LIB" ] || die "缺少用户级版本部署公共库: $VERSIONED_LIB"

SSH_TARGET="$JM_SSH_USER@$JM_SSH_HOST"
rsh() { ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$SSH_TARGET" "$@"; }
rcp() { scp -q -o BatchMode=yes -o StrictHostKeyChecking=accept-new -P "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$1" "$SSH_TARGET:$2"; }

echo "[1/3] 探测目标机 $SSH_TARGET ..."
PROBE=$(rsh "id -u; printf '%s\n' \"\$HOME\"; if command -v systemctl >/dev/null 2>&1; then echo yes; else echo no; fi") \
    || die "SSH 连接失败，请确认目标机可达及认证配置"
probe_line() { printf '%s\n' "$PROBE" | sed -n "${1}p"; }
R_UID=$(probe_line 1); R_HOME=$(probe_line 2); R_SYSTEMD=$(probe_line 3)
[ "$R_SYSTEMD" = "yes" ] || die "目标机无 systemd"

if [ "$JM_SERVICE_SCOPE" = "auto" ] && [ "$R_UID" = "0" ]; then
    die "auto 在当前登录身份会落为 system 档；请显式设 JM_SERVICE_SCOPE=user"
fi
INSTALL_DIR="${JM_INSTALL_DIR:-$R_HOME/jianmanager}"
SVC=$(worker_service_name "$INSTALL_DIR")
UNIT="$R_HOME/.config/systemd/user/$SVC.service"
UNIT_EXISTS=$(rsh "if [ -f '$UNIT' ]; then echo yes; else echo no; fi")
[ "$UNIT_EXISTS" = "yes" ] || die "未找到用户级服务 $SVC，无法回滚"

echo "[2/3] 校验目标版本 $RELEASE_NAME 并切换 current ..."
RTMP=$(rsh "mktemp -d /tmp/jm-rollback.XXXXXX")
cleanup() { rsh "rm -rf '$RTMP'" 2>/dev/null || true; }
trap cleanup EXIT
rcp "$VERSIONED_LIB" "$RTMP/versioned-user-layout.sh"

if ! rsh "sh -s -- '$RTMP' '$INSTALL_DIR' '$BIN_NAME' '$SVC' '$RELEASE_NAME'" <<'REMOTE'
set -eu
remote_tmp=$1
install_dir=$2
binary_name=$3
service_name=$4
release_name=$5
. "$remote_tmp/versioned-user-layout.sh"
export XDG_RUNTIME_DIR="/run/user/$(id -u)"
unit="$HOME/.config/systemd/user/$service_name.service"
[ -L "$install_dir/current" ] || { echo "错误: 当前安装尚未迁移到版本目录" >&2; exit 1; }
grep -Fqx "ExecStart=$install_dir/current/$binary_name" "$unit" || {
    echo "错误: 服务尚未使用 current 路径，请先重新部署迁移" >&2; exit 1;
}
versioned_verify_manifest "$install_dir/versions/$release_name" "$binary_name" || {
    echo "错误: 目标版本不存在或清单校验失败" >&2; exit 1;
}
previous=$(readlink "$install_dir/current")
case "$previous" in versions/*) : ;; *) echo "错误: current 指向无效" >&2; exit 1 ;; esac
switched=0
restore_after_failure() {
    status=$?
    trap - 0
    if [ "$status" -ne 0 ]; then
        if [ "$switched" = "1" ]; then
            versioned_restore_current "$install_dir" "$previous" || true
        fi
        systemctl --user daemon-reload || true
        systemctl --user start "$service_name" || true
    fi
    exit "$status"
}
trap restore_after_failure 0
systemctl --user stop "$service_name" 2>/dev/null || true
versioned_switch_current "$install_dir" "$release_name"
switched=1
systemctl --user daemon-reload
if systemctl --user start "$service_name" && systemctl --user is-active "$service_name" >/dev/null 2>&1; then
    trap - 0
    exit 0
fi
echo "错误: 回滚后的 Worker 未能启动，正在恢复原 current" >&2
exit 1
REMOTE
then
    die "回滚失败，已尝试恢复原 current"
fi

echo "[3/3] 验证服务状态 ..."
ACTIVE=$(rsh "export XDG_RUNTIME_DIR=/run/user/\$(id -u); systemctl --user is-active '$SVC' 2>/dev/null" || true)
if [ "$ACTIVE" = "active" ]; then
    echo "完成: $SVC 已回滚到 $RELEASE_NAME"
else
    rsh "export XDG_RUNTIME_DIR=/run/user/\$(id -u); journalctl --user -u '$SVC' -n 40 --no-pager" >&2 || true
    die "回滚后服务状态异常: ${ACTIVE:-unknown}"
fi
