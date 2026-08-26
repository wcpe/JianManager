#!/bin/sh
# JianManager Control Plane 用户级版本化部署回滚脚本（FR-410）。
set -eu

SVC="jianmanager-cp"
BIN_NAME="jianmanager-cp"
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
VERSIONED_HELPER="$SCRIPT_DIR/lib/versioned-user-layout.sh"

JM_SSH_HOST="${JM_SSH_HOST:-}"
JM_SSH_PORT="${JM_SSH_PORT:-22}"
JM_SSH_USER="${JM_SSH_USER:-root}"
JM_SSH_KEY="${JM_SSH_KEY:-}"
JM_INSTALL_DIR="${JM_INSTALL_DIR:-}"
JM_CP_HTTP_PORT="${JM_CP_HTTP_PORT:-8080}"
JM_SERVICE_SCOPE="${JM_SERVICE_SCOPE:-user}"

die() { echo "错误: $*" >&2; exit 1; }

RELEASE_NAME="${1:-}"
[ -n "$RELEASE_NAME" ] || die "用法: JM_SSH_HOST=<目标> $0 <版本目录名>"
case "$RELEASE_NAME" in *[!A-Za-z0-9._+-]*|''|.|..) die "版本目录名不合法" ;; esac
case "$JM_SERVICE_SCOPE" in user) : ;; *) die "回滚脚本仅支持 JM_SERVICE_SCOPE=user" ;; esac
[ -n "$JM_SSH_HOST" ] || die "缺少 JM_SSH_HOST"
[ -f "$VERSIONED_HELPER" ] || die "缺少用户级版本部署公共函数: $VERSIONED_HELPER"

TARGET="$JM_SSH_USER@$JM_SSH_HOST"
rsh() { ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$TARGET" "$@"; }
rcp() { scp -q -o BatchMode=yes -o StrictHostKeyChecking=accept-new -P "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$1" "$TARGET:$2"; }

echo "[1/3] 探测用户级 CP 部署 ..."
PROBE=$(rsh "id -u; printf '%s\\n' \"\$HOME\"; if [ -f \"\$HOME/.config/systemd/user/$SVC.service\" ]; then echo yes; else echo no; fi") || die "SSH 连接失败"
probe_line() { printf '%s\n' "$PROBE" | sed -n "${1}p"; }
R_UID=$(probe_line 1)
R_HOME=$(probe_line 2)
R_UNIT=$(probe_line 3)
[ "$R_UNIT" = "yes" ] || die "目标机不存在用户级 $SVC.service"
case "$R_UID" in ''|*[!0-9]*) die "目标机用户 ID 异常" ;; esac

INSTALL_DIR="${JM_INSTALL_DIR:-$R_HOME/jianmanager-cp}"
PROBE_PORT="$JM_CP_HTTP_PORT"
REMOTE_PORT=$(rsh "awk '/^server:/{s=1;next} /^[a-zA-Z]/{s=0} s&&/port:/{gsub(/[^0-9]/,\"\",\$0);print;exit}' '$INSTALL_DIR/control-plane.yml' 2>/dev/null" || true)
case "$REMOTE_PORT" in ''|*[!0-9]*) : ;; *) PROBE_PORT="$REMOTE_PORT" ;; esac

RTMP=$(rsh "mktemp -d /tmp/jm-rollback.XXXXXX")
cleanup() { rsh "rm -rf '$RTMP'" 2>/dev/null || true; }
trap cleanup EXIT
rcp "$VERSIONED_HELPER" "$RTMP/versioned-user-layout.sh"

echo "[2/3] 切换 current 到 $RELEASE_NAME ..."
ROLLBACK="set -e
export XDG_RUNTIME_DIR=/run/user/\$(id -u)
. '$RTMP/versioned-user-layout.sh'
versioned_verify_manifest '$INSTALL_DIR/versions/$RELEASE_NAME' '$BIN_NAME' || { echo '错误: 目标版本清单或二进制校验失败' >&2; exit 1; }
[ -L '$INSTALL_DIR/current' ] || { echo '错误: 当前部署不存在 current 链接' >&2; exit 1; }
previous_current=\$(readlink '$INSTALL_DIR/current')
case \"\$previous_current\" in
    versions/*) versioned_verify_manifest \"$INSTALL_DIR/\$previous_current\" '$BIN_NAME' || { echo '错误: 当前版本制品校验失败' >&2; exit 1; } ;;
    *) echo '错误: current 不是受管版本目录' >&2; exit 1 ;;
esac
versioned_switch_current '$INSTALL_DIR' '$RELEASE_NAME'
systemctl --user daemon-reload
systemctl --user restart '$SVC'
printf 'VERSIONED_PREVIOUS_CURRENT=%s\\n' \"\$previous_current\""
ROLLBACK_RESULT=$(printf '%s\n' "$ROLLBACK" | rsh "sh -s")
printf '%s\n' "$ROLLBACK_RESULT"
PREVIOUS_CURRENT=$(printf '%s\n' "$ROLLBACK_RESULT" | sed -n 's/^VERSIONED_PREVIOUS_CURRENT=//p' | sed -n '1p')

echo "[3/3] HTTP 探活验证（端口 $PROBE_PORT，最长 30s）..."
HC="i=0
while [ \$i -lt 30 ]; do
    if command -v curl >/dev/null 2>&1; then
        curl -fsS -o /dev/null \"http://127.0.0.1:$PROBE_PORT/\" 2>/dev/null && { echo ok; exit 0; }
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O /dev/null \"http://127.0.0.1:$PROBE_PORT/\" 2>/dev/null && { echo ok; exit 0; }
    else
        echo no-http-client; exit 0
    fi
    i=\$((i+1)); sleep 1
done
echo timeout; exit 1"
HC_RESULT=$(printf '%s\n' "$HC" | rsh "sh -s" || true)
case "$HC_RESULT" in
    ok|no-http-client)
        [ "$HC_RESULT" = "no-http-client" ] && echo "警告: 目标机无 curl/wget，已跳过 HTTP 探活" >&2
        echo "完成: $SVC 已回滚到 $RELEASE_NAME"
        ;;
    *)
        echo "回滚后的 HTTP 探活失败，正在恢复原 current ..." >&2
        if [ -n "$PREVIOUS_CURRENT" ]; then
            RESTORE="set -e
export XDG_RUNTIME_DIR=/run/user/\$(id -u)
. '$RTMP/versioned-user-layout.sh'
versioned_restore_current '$INSTALL_DIR' '$PREVIOUS_CURRENT'
systemctl --user daemon-reload
systemctl --user restart '$SVC'"
            printf '%s\n' "$RESTORE" | rsh "sh -s" || true
        fi
        die "HTTP 探活失败（$HC_RESULT），已尝试恢复原版本"
        ;;
esac
