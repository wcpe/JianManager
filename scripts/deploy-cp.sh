#!/bin/sh
# JianManager Control Plane（面板）SSH 推送式部署脚本（FR-277，见 ADR-063）。在操作机执行。
#
# 首次部署 = scp 本地 dist 二进制 → 建目录 → 生成最小 control-plane.yml（端口 + sqlite +
#            dev_mode:false，其余吃程序默认；JWT/WS 密钥不写，CP 生产态自动生成，ADR-061）
#            → 写 systemd unit → 启动 → HTTP 探活。
# 更新部署 = stop → 旧二进制留 .bak → 新二进制就位 → start；不重写 control-plane.yml 与
#            unit（既有配置视为运维现场，不覆盖）。
# 首次 / 更新由远端有无对应 systemd unit 自动判定；幂等可重复执行。
#
# 服务档位（JM_SERVICE_SCOPE，ADR-063 §2）：
#   system  /etc/systemd/system + systemctl（root 直连，或非 root 免密 sudo 自动提权）
#   user    ~/.config/systemd/user + systemctl --user（纯普通用户；强制 linger 保常驻）
#   auto    默认：root 或免密 sudo → system，否则 → user
#
# 配置全经 JM_* 环境变量（与目标机二进制消费的 JIANMANAGER_* 命名空间隔离）：
#   JM_SSH_HOST       目标主机 IP/域名（必填）
#   JM_SSH_PORT       SSH 端口（默认 22）
#   JM_SSH_USER       SSH 用户（默认 root）
#   JM_SSH_KEY        SSH 私钥路径（空=默认密钥链/agent）
#   JM_SERVICE_SCOPE  system|user|auto（默认 auto）
#   JM_DIST_DIR       本地产物目录（默认 <仓根>/dist）
#   JM_BUILD          1=产物缺失时自动 make dist（默认 0=报错提示）
#   JM_INSTALL_DIR    安装目录（默认 system=/opt/jianmanager-cp，user=~/jianmanager-cp）
#   JM_DATA_DIR       数据根（默认 <install-dir>/data）
#   JM_CP_HTTP_PORT   面板 HTTP 端口（默认 8080）
#   JM_CP_GRPC_PORT   面板 gRPC 端口（默认 9100）
#
# 用法: JM_SSH_HOST=1.2.3.4 [JM_*=…] scripts/deploy-cp.sh [--dry-run]
#   --dry-run（或 JM_DRY_RUN=1）只打印部署计划，不连接目标机。
set -eu

SVC="jianmanager-cp"
BIN_NAME="jianmanager-cp"

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
JM_CP_HTTP_PORT="${JM_CP_HTTP_PORT:-8080}"
JM_CP_GRPC_PORT="${JM_CP_GRPC_PORT:-9100}"
DRY_RUN="${JM_DRY_RUN:-0}"
[ "${1:-}" = "--dry-run" ] && DRY_RUN="1"

die() { echo "错误: $*" >&2; exit 1; }

case "$JM_SERVICE_SCOPE" in
    system|user|auto) : ;;
    *) die "JM_SERVICE_SCOPE 仅支持 system|user|auto（收到: $JM_SERVICE_SCOPE）" ;;
esac
[ -n "$JM_SSH_HOST" ] || die "缺少 JM_SSH_HOST（目标主机）。示例: JM_SSH_HOST=1.2.3.4 $0"

TARGET="$JM_SSH_USER@$JM_SSH_HOST"

# 同 deploy-worker.sh：BatchMode 禁密码交互、accept-new 首连 TOFU 记 host key。
rsh() { ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$TARGET" "$@"; }
rcp() { scp -q -o BatchMode=yes -o StrictHostKeyChecking=accept-new -P "$JM_SSH_PORT" ${JM_SSH_KEY:+-i "$JM_SSH_KEY"} "$1" "$TARGET:$2"; }

# ---- dry-run：只打印计划 ----
if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] 目标: $TARGET 端口 $JM_SSH_PORT 密钥 ${JM_SSH_KEY:-<默认密钥链>}"
    echo "[dry-run] 服务档位: $JM_SERVICE_SCOPE · HTTP $JM_CP_HTTP_PORT · gRPC $JM_CP_GRPC_PORT"
    BIN_GUESS="$JM_DIST_DIR/control-plane-linux-amd64"
    if [ -f "$BIN_GUESS" ]; then
        echo "[dry-run] 本地产物: $BIN_GUESS (存在)"
    else
        echo "[dry-run] 本地产物: $BIN_GUESS (缺失，JM_BUILD=$JM_BUILD$([ "$JM_BUILD" = "1" ] && echo '，将自动 make dist' || echo '，实跑将报错'))"
    fi
    echo "[dry-run] 步骤: 远端探测(架构/权限/有无 unit) → scp 二进制 → 首次: 建目录+最小 control-plane.yml+unit+启动 / 更新: stop→留 .bak→换二进制→start → HTTP 探活验证"
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

SCOPE="$JM_SERVICE_SCOPE"
if [ "$SCOPE" = "auto" ]; then
    if [ "$R_UID" = "0" ] || [ "$R_SUDO" = "yes" ]; then SCOPE="system"; else SCOPE="user"; fi
fi
if [ "$SCOPE" = "system" ] && [ "$R_UID" != "0" ] && [ "$R_SUDO" != "yes" ]; then
    die "system 档需要 root 或免密 sudo；当前用户 $JM_SSH_USER 二者皆无。可改 JM_SERVICE_SCOPE=user"
fi
[ "$SCOPE" = "system" ] && [ "$R_USERUNIT" = "yes" ] && die "目标机已有 user 档部署（~/.config/systemd/user/$SVC.service），请以同一普通用户 + JM_SERVICE_SCOPE=user 更新"
[ "$SCOPE" = "user" ] && [ "$R_SYSUNIT" = "yes" ] && die "目标机已有 system 档部署（/etc/systemd/system/$SVC.service），请以 root/免密 sudo 用户更新"

NEED_SUDO="0"; [ "$SCOPE" = "system" ] && [ "$R_UID" != "0" ] && NEED_SUDO="1"
if [ "$SCOPE" = "user" ]; then
    SC="systemctl --user"; XDG="export XDG_RUNTIME_DIR=/run/user/\$(id -u);"
    UNIT_EXISTS="$R_USERUNIT"
    UNIT_DIR="$R_HOME/.config/systemd/user"
    WANTED_BY="default.target"
    DEF_INSTALL="$R_HOME/jianmanager-cp"
else
    SC="systemctl"; XDG=":"
    UNIT_EXISTS="$R_SYSUNIT"
    UNIT_DIR="/etc/systemd/system"
    WANTED_BY="multi-user.target"
    DEF_INSTALL="/opt/jianmanager-cp"
fi
INSTALL_DIR="${JM_INSTALL_DIR:-$DEF_INSTALL}"
DATA_DIR="${JM_DATA_DIR:-$INSTALL_DIR/data}"
if [ "$UNIT_EXISTS" = "yes" ]; then MODE="update"; else MODE="install"; fi
echo "      架构 $ARCH · 档位 $SCOPE$([ "$NEED_SUDO" = "1" ] && echo '(sudo)' || true) · $([ "$MODE" = "update" ] && echo 更新部署 || echo 首次部署) · 安装目录 $INSTALL_DIR"

# 探活端口：更新部署从远端已有 control-plane.yml 的 server.port 读取（不重传 JM_CP_HTTP_PORT
# 也能探对端口，且避免默认 8080 与实际端口不符导致误报）；读不到才回退 JM_CP_HTTP_PORT。
# 首次部署直接用 JM_CP_HTTP_PORT（正是脚本即将写入 yml 的值）。
PROBE_PORT="$JM_CP_HTTP_PORT"
if [ "$MODE" = "update" ]; then
    # 取 server: 段内首个 port:（避开 grpc: 段的同名 port）。
    RP=$(rsh "awk '/^server:/{s=1;next} /^[a-zA-Z]/{s=0} s&&/port:/{gsub(/[^0-9]/,\"\",\$0);print;exit}' '$INSTALL_DIR/control-plane.yml' 2>/dev/null" || true)
    case "$RP" in
        ''|*[!0-9]*) : ;;   # 空或非纯数字，保留默认回退
        *) PROBE_PORT="$RP" ;;
    esac
fi

# ---- 本地产物 ----
BIN_LOCAL="$JM_DIST_DIR/control-plane-linux-$ARCH"
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

# ---- 推送 ----
echo "[3/5] 推送产物到目标机 ..."
RTMP=$(rsh "mktemp -d /tmp/jm-deploy.XXXXXX")
cleanup() { rsh "rm -rf '$RTMP'" 2>/dev/null || true; }
trap cleanup EXIT
rcp "$BIN_LOCAL" "$RTMP/$BIN_NAME"

# ---- 首次 / 更新 ----
if [ "$MODE" = "install" ]; then
    echo "[4/5] 首次部署：建目录 + 最小 control-plane.yml + systemd unit + 启动"
    # 远端首装脚本：本地展开配置值；\$ 开头的是远端运行期求值（linger/XDG）。
    # yml 仅在不存在时写（重跑幂等）；密钥零写入——CP 生产态自动生成持久化（ADR-061）。
    LINGER_BLOCK=":"
    if [ "$SCOPE" = "user" ]; then
        LINGER_BLOCK="export XDG_RUNTIME_DIR=/run/user/\$(id -u)
if command -v loginctl >/dev/null 2>&1; then
    if [ \"\$(loginctl show-user \"\$(id -un)\" --property=Linger --value 2>/dev/null)\" != \"yes\" ]; then
        if loginctl enable-linger \"\$(id -un)\" 2>/dev/null; then
            echo \"      已为用户 \$(id -un) 开启 linger（断连后服务常驻）\"
        else
            echo \"错误: 用户 \$(id -un) 未开启 linger 且无权自开，user 档服务在 SSH 断开后会被杀。\" >&2
            echo \"      请让管理员执行一次: loginctl enable-linger \$(id -un)  然后重试。\" >&2
            exit 1
        fi
    fi
else
    echo \"警告: 无 loginctl，无法确认 linger 状态；若 SSH 断开后服务消失，请管理员开启 linger。\" >&2
fi
mkdir -p '$UNIT_DIR'"
    fi
    INSTALL_SCRIPT="set -e
$LINGER_BLOCK
mkdir -p '$INSTALL_DIR' '$DATA_DIR'
mv -f '$RTMP/$BIN_NAME' '$INSTALL_DIR/$BIN_NAME'
chmod +x '$INSTALL_DIR/$BIN_NAME'
if [ ! -f '$INSTALL_DIR/control-plane.yml' ]; then
cat > '$INSTALL_DIR/control-plane.yml' <<'YML_EOF'
# 由 deploy-cp.sh 生成的最小配置（FR-277）。其余配置项均取程序默认值，
# 完整样例见仓库 configs/control-plane.yml。JWT/WS 密钥无需配置：生产态自动生成持久化。
server:
  host: 0.0.0.0
  port: $JM_CP_HTTP_PORT
  dev_mode: false

grpc:
  port: $JM_CP_GRPC_PORT

database:
  driver: sqlite
  dsn: $DATA_DIR/jianmanager.db
YML_EOF
fi
cat > '$UNIT_DIR/$SVC.service' <<'UNIT_EOF'
[Unit]
Description=JianManager Control Plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/$BIN_NAME $INSTALL_DIR/control-plane.yml
Environment=JIANMANAGER_DATA_DIR=$DATA_DIR
Restart=always
RestartSec=5

[Install]
WantedBy=$WANTED_BY
UNIT_EOF
$SC daemon-reload
$SC enable $SVC >/dev/null 2>&1 || true
$SC restart $SVC"
    # 内嵌 heredoc 定界符带引号（<<'YML_EOF'）：配置值已在本地拼串时展开完毕，
    # 远端 sh 不得再做任何展开。
    if [ "$NEED_SUDO" = "1" ]; then
        printf '%s\n' "$INSTALL_SCRIPT" | rsh "sudo -n sh -s"
    else
        printf '%s\n' "$INSTALL_SCRIPT" | rsh "sh -s"
    fi
else
    echo "[4/5] 更新部署：停服务 → 旧二进制留 .bak → 换新 → 重启（control-plane.yml/unit/数据不动）"
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

# ---- 验证：远端本机 HTTP 探活（避免防火墙干扰判定）----
echo "[5/5] HTTP 探活验证（端口 $PROBE_PORT，最长 30s）..."
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
    ok)
        echo "完成: $SVC active（$MODE，$SCOPE 档），面板 http://$JM_SSH_HOST:$PROBE_PORT 可访问。"
        if [ "$MODE" = "install" ]; then
            echo ""
            echo "下一步:"
            echo "  1. 浏览器打开 http://$JM_SSH_HOST:$PROBE_PORT 完成首启引导（创建管理员）"
            echo "  2. 面板「节点 → 添加节点」签发 enrollment token"
            echo "  3. 部署节点: JM_SSH_HOST=<节点机> JM_CONTROL_PLANE=$JM_SSH_HOST:$JM_CP_GRPC_PORT JM_ENROLL_TOKEN=<jmet_...> scripts/deploy-worker.sh"
        fi
        ;;
    no-http-client)
        echo "警告: 目标机无 curl/wget，跳过 HTTP 探活；服务状态:" >&2
        rsh "$XDG $SC is-active $SVC" || true
        ;;
    *)
        echo "HTTP 探活失败（$HC_RESULT），最近日志:" >&2
        if [ "$SCOPE" = "user" ]; then
            rsh "$XDG journalctl --user -u $SVC -n 40 --no-pager" >&2 || true
        elif [ "$NEED_SUDO" = "1" ]; then
            rsh "sudo -n journalctl -u $SVC -n 40 --no-pager" >&2 || true
        else
            rsh "journalctl -u $SVC -n 40 --no-pager" >&2 || true
        fi
        exit 1
        ;;
esac
