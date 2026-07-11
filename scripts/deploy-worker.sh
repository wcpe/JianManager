#!/bin/sh
# JianManager Worker 节点 SSH 推送式部署脚本（FR-277，见 ADR-063）。在操作机执行。
#
# 定位：传输 + 编排，零上线逻辑——
#   首次部署 = scp 本地 dist 二进制 + 仓内 install-worker.sh → 远端执行
#              install-worker.sh --binary … --service（上线语义全走 ADR-051：worker
#              自配 setup、注册、token 不落普通文件）。
#   更新部署 = stop → 旧二进制留 .bak → 新二进制就位 → start；不碰 worker.yml /
#              node-identity.json / unit（身份与配置保留，重连无需 token）。
#   首次 / 更新由远端有无对应 systemd unit 自动判定；幂等可重复执行。
#
# 服务档位（JM_SERVICE_SCOPE，ADR-063 §2）：
#   system  /etc/systemd/system + systemctl（root 直连，或非 root 免密 sudo 自动提权）
#   user    ~/.config/systemd/user + systemctl --user（纯普通用户；强制 linger 保常驻）
#   auto    默认：root 或免密 sudo → system，否则 → user
#
# 配置全经 JM_* 环境变量（与目标机二进制消费的 JIANMANAGER_* 命名空间隔离）：
#   JM_SSH_HOST          目标主机 IP/域名（必填）
#   JM_SSH_PORT          SSH 端口（默认 22）
#   JM_SSH_USER          SSH 用户（默认 root）
#   JM_SSH_KEY           SSH 私钥路径（空=默认密钥链/agent）
#   JM_SERVICE_SCOPE     system|user|auto（默认 auto）
#   JM_DIST_DIR          本地产物目录（默认 <仓根>/dist）
#   JM_BUILD             1=产物缺失时自动 make dist（默认 0=报错提示）
#   JM_INSTALL_DIR       安装目录（默认 system=/opt/jianmanager，user=~/jianmanager）
#   JM_DATA_DIR          数据根（默认 <install-dir>/data）
#   JM_CONTROL_PLANE     CP gRPC 地址 host:port（首次部署必填）
#   JM_ENROLL_TOKEN      一次性 enrollment token（首次部署必填；更新不需要）
#   JM_NODE_NAME         节点名（可选，空=CP 预设名）
#   JM_WORKER_GRPC_PORT  Worker gRPC 端口（默认 9101）
#   JM_WORKER_WS_PORT    Worker WS 端口（默认 9102）
#
# 用法: JM_SSH_HOST=1.2.3.4 [JM_*=…] scripts/deploy-worker.sh [--dry-run]
#   --dry-run（或 JM_DRY_RUN=1）只打印部署计划，不连接目标机。
set -eu

SVC="jianmanager-worker"
BIN_NAME="jianmanager-worker"

# ---- 配置解析 ----
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

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
JM_WORKER_GRPC_PORT="${JM_WORKER_GRPC_PORT:-9101}"
JM_WORKER_WS_PORT="${JM_WORKER_WS_PORT:-9102}"
DRY_RUN="${JM_DRY_RUN:-0}"
[ "${1:-}" = "--dry-run" ] && DRY_RUN="1"

die() { echo "错误: $*" >&2; exit 1; }

case "$JM_SERVICE_SCOPE" in
    system|user|auto) : ;;
    *) die "JM_SERVICE_SCOPE 仅支持 system|user|auto（收到: $JM_SERVICE_SCOPE）" ;;
esac
[ -n "$JM_SSH_HOST" ] || die "缺少 JM_SSH_HOST（目标主机）。示例: JM_SSH_HOST=1.2.3.4 $0"

TARGET="$JM_SSH_USER@$JM_SSH_HOST"

# rsh/rcp：统一 ssh/scp 选项。BatchMode 禁密码交互（密钥认证不成直接失败），
# accept-new 首连自动记 host key（TOFU），已知主机键变更仍会拒绝。
# ${JM_SSH_KEY:+…} 惯用法：变量空时整段消失，避免空 -i 参数。
rsh() { ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$TARGET" "$@"; }
rcp() { scp -q -o BatchMode=yes -o StrictHostKeyChecking=accept-new -P "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$1" "$TARGET:$2"; }

# ---- dry-run：只打印计划 ----
if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] 目标: $TARGET 端口 $JM_SSH_PORT 密钥 ${JM_SSH_KEY:-<默认密钥链>}"
    echo "[dry-run] 服务档位: $JM_SERVICE_SCOPE"
    BIN_GUESS="$JM_DIST_DIR/worker-linux-amd64"
    if [ -f "$BIN_GUESS" ]; then
        echo "[dry-run] 本地产物: $BIN_GUESS (存在)"
    else
        echo "[dry-run] 本地产物: $BIN_GUESS (缺失，JM_BUILD=$JM_BUILD$([ "$JM_BUILD" = "1" ] && echo '，将自动 make dist' || echo '，实跑将报错'))"
    fi
    echo "[dry-run] JM_CONTROL_PLANE: ${JM_CONTROL_PLANE:-<未设>（首次部署必需）}"
    echo "[dry-run] JM_ENROLL_TOKEN: $([ -n "$JM_ENROLL_TOKEN" ] && echo 已设 || echo '<未设>（首次部署必需）')"
    echo "[dry-run] 步骤: 远端探测(架构/权限/有无 unit) → scp 二进制+install-worker.sh → 首次: install-worker.sh --binary … --service --service-scope <档位> / 更新: stop→留 .bak→换二进制→start → is-active 验证"
    exit 0
fi

# ---- 远端探测（一次 ssh 收全）----
echo "[1/5] 探测目标机 $TARGET ..."
PROBE=$(rsh "uname -m; id -u; printf '%s\n' \"\$HOME\"; \
if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then echo yes; else echo no; fi; \
if command -v systemctl >/dev/null 2>&1; then echo yes; else echo no; fi; \
if [ -f /etc/systemd/system/$SVC.service ]; then echo yes; else echo no; fi; \
if [ -f \"\$HOME/.config/systemd/user/$SVC.service\" ]; then echo yes; else echo no; fi") \
    || die "SSH 连接失败（$TARGET 端口 $JM_SSH_PORT）。请确认: ①主机可达 ②公钥已放入目标机 ~/.ssh/authorized_keys ③JM_SSH_KEY 指向正确私钥"

probe_line() { printf '%s\n' "$PROBE" | sed -n "${1}p"; }
R_ARCH=$(probe_line 1); R_UID=$(probe_line 2); R_HOME=$(probe_line 3)
R_SUDO=$(probe_line 4); R_SYSTEMD=$(probe_line 5); R_SYSUNIT=$(probe_line 6); R_USERUNIT=$(probe_line 7)

[ "$R_SYSTEMD" = "yes" ] || die "目标机无 systemd（systemctl 不存在），本脚本仅支持 systemd 主机"
case "$R_ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "不支持的目标机架构: $R_ARCH" ;;
esac

# 档位落定（ADR-063 §2）
SCOPE="$JM_SERVICE_SCOPE"
if [ "$SCOPE" = "auto" ]; then
    if [ "$R_UID" = "0" ] || [ "$R_SUDO" = "yes" ]; then SCOPE="system"; else SCOPE="user"; fi
fi
if [ "$SCOPE" = "system" ] && [ "$R_UID" != "0" ] && [ "$R_SUDO" != "yes" ]; then
    die "system 档需要 root 或免密 sudo；当前用户 $JM_SSH_USER 二者皆无。可改 JM_SERVICE_SCOPE=user"
fi
# 档位与既有部署冲突检测：换档更新会产生两套服务抢同一实例，直接拒绝。
[ "$SCOPE" = "system" ] && [ "$R_USERUNIT" = "yes" ] && die "目标机已有 user 档部署（~/.config/systemd/user/$SVC.service），请以同一普通用户 + JM_SERVICE_SCOPE=user 更新"
[ "$SCOPE" = "user" ] && [ "$R_SYSUNIT" = "yes" ] && die "目标机已有 system 档部署（/etc/systemd/system/$SVC.service），请以 root/免密 sudo 用户更新"

NEED_SUDO="0"; [ "$SCOPE" = "system" ] && [ "$R_UID" != "0" ] && NEED_SUDO="1"
if [ "$SCOPE" = "user" ]; then
    SC="systemctl --user"; XDG="export XDG_RUNTIME_DIR=/run/user/\$(id -u);"
    UNIT_EXISTS="$R_USERUNIT"
    DEF_INSTALL="$R_HOME/jianmanager"
else
    SC="systemctl"; XDG=":"
    UNIT_EXISTS="$R_SYSUNIT"
    DEF_INSTALL="/opt/jianmanager"
fi
INSTALL_DIR="${JM_INSTALL_DIR:-$DEF_INSTALL}"
DATA_DIR="${JM_DATA_DIR:-$INSTALL_DIR/data}"
if [ "$UNIT_EXISTS" = "yes" ]; then MODE="update"; else MODE="install"; fi
echo "      架构 $ARCH · 档位 $SCOPE$([ "$NEED_SUDO" = "1" ] && echo '(sudo)' || true) · $([ "$MODE" = "update" ] && echo 更新部署 || echo 首次部署) · 安装目录 $INSTALL_DIR"

# ---- 本地产物 ----
BIN_LOCAL="$JM_DIST_DIR/worker-linux-$ARCH"
if [ ! -f "$BIN_LOCAL" ]; then
    if [ "$JM_BUILD" = "1" ]; then
        echo "[2/5] 本地产物缺失，执行 make dist ..."
        (cd "$REPO_ROOT" && make dist) || die "make dist 失败"
        [ -f "$BIN_LOCAL" ] || die "make dist 后仍无 $BIN_LOCAL（arm64 需扩 dist 目标产物，见 spec §6）"
    else
        die "本地产物缺失: $BIN_LOCAL。请先在仓根执行 make dist，或设 JM_BUILD=1 自动构建"
    fi
else
    echo "[2/5] 本地产物就绪: $BIN_LOCAL"
fi

# 首次部署必填项
if [ "$MODE" = "install" ]; then
    [ -n "$JM_CONTROL_PLANE" ] || die "首次部署缺少 JM_CONTROL_PLANE（CP gRPC 地址 host:port）"
    [ -n "$JM_ENROLL_TOKEN" ] || die "首次部署缺少 JM_ENROLL_TOKEN（面板「添加节点」签发的一次性 jmet_ 令牌）"
fi

# ---- 推送 ----
echo "[3/5] 推送产物到目标机 ..."
RTMP=$(rsh "mktemp -d /tmp/jm-deploy.XXXXXX")
cleanup() { rsh "rm -rf '$RTMP'" 2>/dev/null || true; }
trap cleanup EXIT
rcp "$BIN_LOCAL" "$RTMP/$BIN_NAME"
rcp "$REPO_ROOT/scripts/install-worker.sh" "$RTMP/install-worker.sh"

# ---- 首次 / 更新 ----
if [ "$MODE" = "install" ]; then
    echo "[4/5] 首次部署：远端执行 install-worker.sh（ADR-051 语义：worker 自配 setup + 注册 + 常驻）"
    CMD="sh '$RTMP/install-worker.sh' --binary '$RTMP/$BIN_NAME' --service --service-scope $SCOPE --control-plane '$JM_CONTROL_PLANE' --install-dir '$INSTALL_DIR' --data-dir '$DATA_DIR' --grpc-port '$JM_WORKER_GRPC_PORT' --ws-port '$JM_WORKER_WS_PORT'"
    [ -n "$JM_NODE_NAME" ] && CMD="$CMD --name '$JM_NODE_NAME'"
    # token 经 env 交给远端（install-worker.sh 支持 JIANMANAGER_ENROLL_TOKEN 缺省），不进远端命令行参数。
    if [ "$NEED_SUDO" = "1" ]; then
        rsh "sudo -n env JIANMANAGER_ENROLL_TOKEN='$JM_ENROLL_TOKEN' $CMD"
    else
        rsh "JIANMANAGER_ENROLL_TOKEN='$JM_ENROLL_TOKEN' $CMD"
    fi
else
    echo "[4/5] 更新部署：停服务 → 旧二进制留 .bak → 换新 → 重启（配置/身份/数据不动）"
    UP="set -e
$XDG
$SC stop $SVC
if [ -f '$INSTALL_DIR/$BIN_NAME' ]; then mv -f '$INSTALL_DIR/$BIN_NAME' '$INSTALL_DIR/$BIN_NAME.bak'; fi
mv -f '$RTMP/$BIN_NAME' '$INSTALL_DIR/$BIN_NAME'
chmod +x '$INSTALL_DIR/$BIN_NAME'
$SC start $SVC"
    if [ "$NEED_SUDO" = "1" ]; then
        printf '%s\n' "$UP" | rsh "sudo -n sh -s"
    else
        printf '%s\n' "$UP" | rsh "sh -s"
    fi
fi

# ---- 验证 ----
echo "[5/5] 验证服务状态 ..."
sleep 2
ACTIVE=$(rsh "$XDG $SC is-active $SVC 2>/dev/null" || true)
if [ "$ACTIVE" = "active" ]; then
    echo "完成: $SVC active（$MODE，$SCOPE 档）。节点将自动连上 CP，请到面板「节点」页确认在线。"
else
    echo "服务状态异常: ${ACTIVE:-unknown}，最近日志:" >&2
    if [ "$SCOPE" = "user" ]; then
        rsh "$XDG journalctl --user -u $SVC -n 40 --no-pager" >&2 || true
    elif [ "$NEED_SUDO" = "1" ]; then
        rsh "sudo -n journalctl -u $SVC -n 40 --no-pager" >&2 || true
    else
        rsh "journalctl -u $SVC -n 40 --no-pager" >&2 || true
    fi
    exit 1
fi
