#!/bin/sh
# JianManager Worker 节点 SSH 推送式部署脚本（FR-410）。在操作机执行。
#
# system 档保持 FR-277 的直装/.bak 更新行为；user 档改用永久版本目录：
# versions/<版本>--<UTC 时间>--<摘要>/，current 原子指向已验证版本。
# Worker 的配置、数据和身份始终保留在安装目录稳定路径，不随版本切换。
#
# 配置全经 JM_* 环境变量：
#   JM_SSH_HOST、JM_SSH_PORT、JM_SSH_USER、JM_SSH_KEY
#   JM_SERVICE_SCOPE=system|user|auto（默认 auto）
#   JM_DIST_DIR、JM_BUILD、JM_INSTALL_DIR、JM_DATA_DIR
#   JM_CONTROL_PLANE、JM_ENROLL_TOKEN、JM_NODE_NAME、JM_WORKER_WS_PORT
#
# 用法: JM_SSH_HOST=1.2.3.4 [JM_*=…] scripts/deploy-worker.sh [--dry-run]
set -eu

BIN_NAME="jianmanager-worker"

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
VERSIONED_LIB="$REPO_ROOT/scripts/lib/versioned-user-layout.sh"

JM_SSH_HOST="${JM_SSH_HOST:-}"
JM_SSH_PORT="${JM_SSH_PORT:-22}"
JM_SSH_USER="${JM_SSH_USER:-root}"
JM_SSH_KEY="${JM_SSH_KEY:-}"
JM_SERVICE_SCOPE="${JM_SERVICE_SCOPE:-auto}"
JM_DIST_DIR="${JM_DIST_DIR:-$REPO_ROOT/dist}"
JM_BUILD="${JM_BUILD:-0}"
JM_INSTALL_DIR="${JM_INSTALL_DIR:-}"
JM_DATA_DIR="${JM_DATA_DIR:-}"
JM_CONTROL_PLANE="${JM_CONTROL_PLANE:-}"
JM_ENROLL_TOKEN="${JM_ENROLL_TOKEN:-}"
JM_NODE_NAME="${JM_NODE_NAME:-}"
JM_WORKER_WS_PORT="${JM_WORKER_WS_PORT:-9102}"
DRY_RUN="${JM_DRY_RUN:-0}"
[ "${1:-}" = "--dry-run" ] && DRY_RUN="1"

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

case "$JM_SERVICE_SCOPE" in
    system|user|auto) : ;;
    *) die "JM_SERVICE_SCOPE 仅支持 system|user|auto（收到: $JM_SERVICE_SCOPE）" ;;
esac
[ -n "$JM_SSH_HOST" ] || die "缺少 JM_SSH_HOST（目标主机）。示例: JM_SSH_HOST=1.2.3.4 $0"
[ -f "$VERSIONED_LIB" ] || die "缺少用户级版本部署公共库: $VERSIONED_LIB"

TARGET="$JM_SSH_USER@$JM_SSH_HOST"
rsh() { ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$TARGET" "$@"; }
rcp() { scp -q -o BatchMode=yes -o StrictHostKeyChecking=accept-new -P "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$1" "$TARGET:$2"; }

if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] 目标: $TARGET 端口 $JM_SSH_PORT 密钥 ${JM_SSH_KEY:-<默认密钥链>}"
    echo "[dry-run] 服务档位: $JM_SERVICE_SCOPE"
    BIN_GUESS="$JM_DIST_DIR/worker-linux-amd64"
    if [ -f "$BIN_GUESS" ]; then
        echo "[dry-run] 本地产物: $BIN_GUESS（存在）"
    else
        echo "[dry-run] 本地产物: $BIN_GUESS（缺失，JM_BUILD=$JM_BUILD）"
    fi
    echo "[dry-run] user 档将永久归档版本、迁移旧根二进制/.bak，并将 current 原子切换到新版本"
    exit 0
fi

echo "[1/5] 探测目标机 $TARGET ..."
PROBE=$(rsh "uname -m; id -u; printf '%s\n' \"\$HOME\"; \
if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then echo yes; else echo no; fi; \
if command -v systemctl >/dev/null 2>&1; then echo yes; else echo no; fi") \
    || die "SSH 连接失败（$TARGET 端口 $JM_SSH_PORT）。请确认目标机可达、SSH 认证及密钥配置"
probe_line() { printf '%s\n' "$PROBE" | sed -n "${1}p"; }
R_ARCH=$(probe_line 1); R_UID=$(probe_line 2); R_HOME=$(probe_line 3)
R_SUDO=$(probe_line 4); R_SYSTEMD=$(probe_line 5)

[ "$R_SYSTEMD" = "yes" ] || die "目标机无 systemd（systemctl 不存在），本脚本仅支持 systemd 主机"
case "$R_ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "不支持的目标机架构: $R_ARCH" ;;
esac

SCOPE="$JM_SERVICE_SCOPE"
if [ "$SCOPE" = "auto" ]; then
    if [ "$R_UID" = "0" ] || [ "$R_SUDO" = "yes" ]; then SCOPE="system"; else SCOPE="user"; fi
fi
if [ "$SCOPE" = "system" ] && [ "$R_UID" != "0" ] && [ "$R_SUDO" != "yes" ]; then
    die "system 档需要 root 或免密 sudo；当前用户 $JM_SSH_USER 二者皆无。可改 JM_SERVICE_SCOPE=user"
fi

NEED_SUDO="0"; [ "$SCOPE" = "system" ] && [ "$R_UID" != "0" ] && NEED_SUDO="1"
if [ "$SCOPE" = "user" ]; then
    SC="systemctl --user"; XDG="export XDG_RUNTIME_DIR=/run/user/\$(id -u);"
    DEF_INSTALL="$R_HOME/jianmanager"
else
    SC="systemctl"; XDG=":"
    DEF_INSTALL="/opt/jianmanager"
fi
INSTALL_DIR="${JM_INSTALL_DIR:-$DEF_INSTALL}"
DATA_DIR="${JM_DATA_DIR:-$INSTALL_DIR/data}"
SVC=$(worker_service_name "$INSTALL_DIR")
UNIT_STATUS=$(rsh "if [ -f '/etc/systemd/system/$SVC.service' ]; then echo system; fi; if [ -f \"\$HOME/.config/systemd/user/$SVC.service\" ]; then echo user; fi")
case "$SCOPE:$UNIT_STATUS" in
    system:*user*) die "目标机已有 user 档部署（$SVC），请以同一普通用户 + JM_SERVICE_SCOPE=user 更新" ;;
    user:*system*) die "目标机已有 system 档部署（$SVC），请以 root/免密 sudo 用户更新" ;;
esac
case "$SCOPE:$UNIT_STATUS" in
    system:*system*) MODE="update" ;;
    user:*user*) MODE="update" ;;
    *) MODE="install" ;;
esac
echo "      架构 $ARCH · 档位 $SCOPE$([ "$NEED_SUDO" = "1" ] && echo '(sudo)' || true) · 服务 $SVC · $([ "$MODE" = "update" ] && echo 更新部署 || echo 首次部署) · 安装目录 $INSTALL_DIR"

BIN_LOCAL="$JM_DIST_DIR/worker-linux-$ARCH"
if [ ! -f "$BIN_LOCAL" ]; then
    if [ "$JM_BUILD" = "1" ]; then
        echo "[2/5] 本地产物缺失，执行 make dist ..."
        (cd "$REPO_ROOT" && make dist) || die "make dist 失败"
        [ -f "$BIN_LOCAL" ] || die "make dist 后仍无 $BIN_LOCAL"
    else
        die "本地产物缺失: $BIN_LOCAL。请先在仓根执行 make dist，或设 JM_BUILD=1 自动构建"
    fi
else
    echo "[2/5] 本地产物就绪: $BIN_LOCAL"
fi
BIN_SHA=$(sha256sum "$BIN_LOCAL" | awk 'NR == 1 { print $1 }')
[ "${#BIN_SHA}" -eq 64 ] || die "无法计算本地产物 SHA-256，请安装 sha256sum"
case "$BIN_SHA" in *[!0123456789abcdef]*) die "本地产物 SHA-256 格式无效" ;; esac

if [ "$MODE" = "install" ]; then
    [ -n "$JM_CONTROL_PLANE" ] || die "首次部署缺少 JM_CONTROL_PLANE（CP gRPC 地址 host:port）"
    [ -n "$JM_ENROLL_TOKEN" ] || die "首次部署缺少 JM_ENROLL_TOKEN（面板「添加节点」签发的一次性 jmet_ 令牌）"
fi

echo "[3/5] 推送产物到目标机 ..."
RTMP=$(rsh "mktemp -d /tmp/jm-deploy.XXXXXX")
cleanup() { rsh "rm -rf '$RTMP'" 2>/dev/null || true; }
trap cleanup EXIT
rcp "$BIN_LOCAL" "$RTMP/$BIN_NAME"
rcp "$REPO_ROOT/scripts/install-worker.sh" "$RTMP/install-worker.sh"
if [ "$SCOPE" = "user" ]; then
    rcp "$VERSIONED_LIB" "$RTMP/versioned-user-layout.sh"
fi
REMOTE_SHA=$(rsh "sha256sum '$RTMP/$BIN_NAME' | awk 'NR == 1 { print \$1 }'")
[ "$REMOTE_SHA" = "$BIN_SHA" ] || die "远端产物 SHA-256 校验失败，已中止部署"

prepare_user_release() {
    rsh "sh -s -- '$RTMP' '$INSTALL_DIR' '$BIN_NAME' '$BIN_SHA'" <<'REMOTE'
set -eu
remote_tmp=$1
install_dir=$2
binary_name=$3
expected_sha256=$4
. "$remote_tmp/versioned-user-layout.sh"
actual_sha256=$(versioned_sha256_file "$remote_tmp/$binary_name")
[ "$actual_sha256" = "$expected_sha256" ]
version=$(versioned_binary_version "$remote_tmp/$binary_name")
versioned_reserve_version_dir "$install_dir" "$version" "$actual_sha256"
mv "$remote_tmp/$binary_name" "$VERSIONED_RELEASE_DIR/$binary_name"
chmod +x "$VERSIONED_RELEASE_DIR/$binary_name"
versioned_write_manifest "$VERSIONED_RELEASE_DIR" "$version" "$actual_sha256"
versioned_verify_manifest "$VERSIONED_RELEASE_DIR" "$binary_name"
printf '%s\n' "$VERSIONED_RELEASE_NAME"
REMOTE
}

current_target() {
    rsh "if [ -L '$INSTALL_DIR/current' ]; then readlink '$INSTALL_DIR/current'; fi"
}

switch_user_current() {
    release=$1
    rsh "sh -s -- '$RTMP' '$INSTALL_DIR' '$release'" <<'REMOTE'
set -eu
remote_tmp=$1
install_dir=$2
release_name=$3
. "$remote_tmp/versioned-user-layout.sh"
versioned_switch_current "$install_dir" "$release_name"
REMOTE
}

archive_orphaned_user_legacy() {
    rsh "sh -s -- '$RTMP' '$INSTALL_DIR' '$BIN_NAME'" <<'REMOTE'
set -eu
remote_tmp=$1
install_dir=$2
binary_name=$3
. "$remote_tmp/versioned-user-layout.sh"
if [ -f "$install_dir/$binary_name" ]; then
    versioned_archive_legacy_binary "$install_dir" "$binary_name" "$install_dir/$binary_name"
    versioned_verify_manifest "$VERSIONED_RELEASE_DIR" "$binary_name"
fi
if [ -f "$install_dir/$binary_name.bak" ]; then
    versioned_archive_legacy_binary "$install_dir" "$binary_name" "$install_dir/$binary_name.bak"
    versioned_verify_manifest "$VERSIONED_RELEASE_DIR" "$binary_name"
fi
REMOTE
}

restore_user_current() {
    previous=$1
    rsh "sh -s -- '$RTMP' '$INSTALL_DIR' '$SVC' '$previous'" <<'REMOTE' || true
set -eu
remote_tmp=$1
install_dir=$2
service_name=$3
previous=$4
export XDG_RUNTIME_DIR="/run/user/$(id -u)"
systemctl --user stop "$service_name" 2>/dev/null || true
if [ -n "$previous" ]; then
    . "$remote_tmp/versioned-user-layout.sh"
    versioned_restore_current "$install_dir" "$previous"
    systemctl --user daemon-reload
    systemctl --user start "$service_name" || true
else
    rm -f "$install_dir/current"
fi
REMOTE
}

activate_user_release() {
    new_release=$1
    previous=$2
    rsh "sh -s -- '$RTMP' '$INSTALL_DIR' '$BIN_NAME' '$SVC' '$new_release' '$previous'" <<'REMOTE'
set -eu
remote_tmp=$1
install_dir=$2
binary_name=$3
service_name=$4
new_release=$5
previous=$6
. "$remote_tmp/versioned-user-layout.sh"
export XDG_RUNTIME_DIR="/run/user/$(id -u)"
unit="$HOME/.config/systemd/user/$service_name.service"
[ -f "$unit" ] || { echo "错误: 未找到用户级服务文件" >&2; exit 1; }
expected_legacy_exec="ExecStart=$install_dir/$binary_name"
expected_current_exec="ExecStart=$install_dir/current/$binary_name"
if grep -Fqx "$expected_current_exec" "$unit"; then
    unit_needs_update=0
elif grep -Fqx "$expected_legacy_exec" "$unit"; then
    unit_needs_update=1
else
    echo "错误: 服务 ExecStart 不属于受支持的部署路径，拒绝覆盖自定义 unit" >&2
    exit 1
fi
switched=0
restore_after_failure() {
    status=$?
    trap - 0
    if [ "$status" -ne 0 ]; then
        if [ "$switched" = "1" ] && [ -n "$previous" ]; then
            versioned_restore_current "$install_dir" "$previous" || true
        fi
        systemctl --user daemon-reload || true
        systemctl --user start "$service_name" || true
    fi
    exit "$status"
}
trap restore_after_failure 0
systemctl --user stop "$service_name" 2>/dev/null || true
legacy_current=""
if [ -f "$install_dir/$binary_name" ]; then
    versioned_archive_legacy_binary "$install_dir" "$binary_name" "$install_dir/$binary_name"
    versioned_verify_manifest "$VERSIONED_RELEASE_DIR" "$binary_name"
    legacy_current="versions/$VERSIONED_RELEASE_NAME"
fi
if [ -f "$install_dir/$binary_name.bak" ]; then
    versioned_archive_legacy_binary "$install_dir" "$binary_name" "$install_dir/$binary_name.bak"
    versioned_verify_manifest "$VERSIONED_RELEASE_DIR" "$binary_name"
fi
if [ -z "$previous" ] && [ -n "$legacy_current" ]; then
    previous=$legacy_current
fi
versioned_switch_current "$install_dir" "$new_release"
switched=1
rm -f "$install_dir/$binary_name" "$install_dir/$binary_name.bak"
if [ "$unit_needs_update" = "1" ]; then
    temporary_unit="$unit.tmp.$$"
    awk -v old="$expected_legacy_exec" -v new="$expected_current_exec" \
        '$0 == old { print new; next } { print }' "$unit" > "$temporary_unit"
    mv -f "$temporary_unit" "$unit"
fi
systemctl --user daemon-reload
systemctl --user start "$service_name"
systemctl --user is-active "$service_name" >/dev/null 2>&1
trap - 0
exit 0
REMOTE
}

if [ "$SCOPE" = "user" ]; then
    echo "[4/5] 用户级版本化部署：归档版本并切换 current ..."
    PREVIOUS_CURRENT=$(current_target || true)
    NEW_RELEASE=$(prepare_user_release) || die "无法准备已校验的 Worker 版本目录"
    if [ "$MODE" = "install" ]; then
        archive_orphaned_user_legacy || die "无法归档既有 Worker 根目录二进制"
        switch_user_current "$NEW_RELEASE" || die "无法切换 Worker 的 current 指针"
        CMD="sh '$RTMP/install-worker.sh' --binary '$INSTALL_DIR/current/$BIN_NAME' --service --service-scope user --service-exec '$INSTALL_DIR/current/$BIN_NAME' --service-name '$SVC' --control-plane '$JM_CONTROL_PLANE' --install-dir '$INSTALL_DIR' --data-dir '$DATA_DIR' --ws-port '$JM_WORKER_WS_PORT'"
        [ -n "$JM_NODE_NAME" ] && CMD="$CMD --name '$JM_NODE_NAME'"
        if ! rsh "JIANMANAGER_ENROLL_TOKEN='$JM_ENROLL_TOKEN' $CMD"; then
            restore_user_current "$PREVIOUS_CURRENT"
            die "首次 Worker 注册或服务启动失败，已恢复此前 current"
        fi
        rsh "rm -f '$INSTALL_DIR/$BIN_NAME' '$INSTALL_DIR/$BIN_NAME.bak'"
    elif ! activate_user_release "$NEW_RELEASE" "$PREVIOUS_CURRENT"; then
        die "Worker 服务启动失败，已尝试恢复此前 current"
    fi
else
    if [ "$MODE" = "install" ]; then
        echo "[4/5] 首次部署：远端执行 install-worker.sh"
        CMD="sh '$RTMP/install-worker.sh' --binary '$RTMP/$BIN_NAME' --service --service-scope system --control-plane '$JM_CONTROL_PLANE' --install-dir '$INSTALL_DIR' --data-dir '$DATA_DIR' --ws-port '$JM_WORKER_WS_PORT'"
        [ -n "$JM_NODE_NAME" ] && CMD="$CMD --name '$JM_NODE_NAME'"
        if [ "$NEED_SUDO" = "1" ]; then
            rsh "sudo -n env JIANMANAGER_ENROLL_TOKEN='$JM_ENROLL_TOKEN' $CMD"
        else
            rsh "JIANMANAGER_ENROLL_TOKEN='$JM_ENROLL_TOKEN' $CMD"
        fi
    else
        echo "[4/5] 更新部署：停服务 → 旧二进制留 .bak → 换新 → 重启（配置/身份/数据不动）"
        UPDATE="set -e
$XDG
$SC stop $SVC
if [ -f '$INSTALL_DIR/$BIN_NAME' ]; then mv -f '$INSTALL_DIR/$BIN_NAME' '$INSTALL_DIR/$BIN_NAME.bak'; fi
mv -f '$RTMP/$BIN_NAME' '$INSTALL_DIR/$BIN_NAME'
chmod +x '$INSTALL_DIR/$BIN_NAME'
$SC start $SVC"
        if [ "$NEED_SUDO" = "1" ]; then
            printf '%s\n' "$UPDATE" | rsh "sudo -n sh -s"
        else
            printf '%s\n' "$UPDATE" | rsh "sh -s"
        fi
    fi
fi

echo "[5/5] 验证服务状态 ..."
sleep 2
ACTIVE=$(rsh "$XDG $SC is-active $SVC 2>/dev/null" || true)
if [ "$ACTIVE" = "active" ]; then
    echo "完成: $SVC active（$MODE，$SCOPE 档）。节点将自动连上 CP，请到面板「节点」页确认在线。"
else
    echo "服务状态异常: ${ACTIVE:-unknown}，最近日志:" >&2
    if [ "$SCOPE" = "user" ]; then
        restore_user_current "$PREVIOUS_CURRENT"
        echo "已恢复此前 current 并尝试重启 Worker 服务。" >&2
    fi
    if [ "$SCOPE" = "user" ]; then
        rsh "$XDG journalctl --user -u $SVC -n 40 --no-pager" >&2 || true
    elif [ "$NEED_SUDO" = "1" ]; then
        rsh "sudo -n journalctl -u $SVC -n 40 --no-pager" >&2 || true
    else
        rsh "journalctl -u $SVC -n 40 --no-pager" >&2 || true
    fi
    exit 1
fi
