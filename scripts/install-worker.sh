#!/bin/sh
# JianManager Worker Node 安装脚本（Linux/macOS，POSIX sh）。
# 见 FR-223 / ADR-051（改写 ADR-020 的安装编排立场）：脚本退化为「取二进制 + 调 worker setup」。
#
# 两阶段（参考 GitHub Actions Runner「下载」与「config+run」分离）：
#   [下载阶段] 取 Worker 二进制。安装目录（或 --binary 指向处）已有完整二进制则跳过下载。
#   [上线阶段] 调 `worker --control-plane ... --token ...`，由 Worker 自己写 worker.yml +
#              携 enrollment token 首注册 + 持久化身份 + 转入 run（worker 免配置自启 setup，FR-222）。
#              本脚本不再自己写 worker.yml（配置归属回到 Worker 自身）。
#   可选 --service 注册 systemd 服务常驻：服务 ExecStart 直接跑 worker（首次 setup 写配置 +
#   持久化身份后，服务重启复用持久化配置，不再依赖 token）。
#
# 阶段控制：
#   --download-only   只下载/准备二进制，不上线（下载与上线分两次执行时用）
#   --skip-download   跳过下载，直接用安装目录已有二进制上线
#   缺省              顺序两段：下载（按需）→ 上线
#
# 一键命令形态（面板「添加节点」生成、直接粘贴）：
#   curl -fsSL <cp>/install-worker.sh | sh -s -- --control-plane <cp-grpc> --token <jmet_...> [--name N] [--service]
#
# enrollment token 一次性、限时；仅经命令行/环境变量传入，绝不写入磁盘（worker setup 同样不落盘）。
# 注册成功后 CP 换发的 node_uuid/node_secret 由 Worker 持久化到 <data-dir>/etc/node-identity.json。
set -eu

# ---- 默认值 ----
CONTROL_PLANE=""        # CP gRPC 地址 host:port（上线阶段必填）
TOKEN=""                # enrollment token 明文（上线阶段必填）
NODE_NAME=""            # 节点名（可选，留空由 Worker/CP 预设名生效）
BINARY=""               # 本地已拷贝的 Worker 二进制路径（离线/内网兜底，跳过下载）
# Worker 二进制下载地址（可选）。缺省走 GitHub Releases latest（ADR-036 产物命名契约：
# worker-<os>-<arch>[.exe]）；离线/内网用 --binary 或 --download-url 覆盖。
DOWNLOAD_URL="https://github.com/wcpe/jianmanager/releases/latest/download"
INSTALL_DIR="/opt/jianmanager"   # 安装目录
DATA_DIR=""             # 数据根（缺省 <install-dir>/data）
GRPC_PORT="9101"        # Worker gRPC 端口
WS_PORT="9102"          # Worker WS 终端端口
INSTALL_SERVICE="0"     # 是否注册 systemd 服务
DOWNLOAD_ONLY="0"       # 仅下载、不上线
SKIP_DOWNLOAD="0"       # 跳过下载、直接上线

usage() {
    cat <<'USAGE'
用法: install-worker.sh --control-plane <host:port> --token <jmet_...> [选项]

上线阶段必填（--download-only 时可省）:
  --control-plane <addr>   Control Plane gRPC 地址 host:port
  --token <jmet_...>       一次性 enrollment token（面板「添加节点」生成）

可选:
  --name <node>            节点名（留空由 Worker/CP 预设名生效）
  --binary <path>          本地 Worker 二进制路径（离线/内网，跳过下载）
  --download-url <url>     Worker 二进制下载基址/地址（默认 GitHub Releases latest）
  --install-dir <dir>      安装目录（默认 /opt/jianmanager）
  --data-dir <dir>         数据根目录（默认 <install-dir>/data）
  --grpc-port <port>       Worker gRPC 端口（默认 9101）
  --ws-port <port>         Worker WS 端口（默认 9102）
  --service                注册 systemd 服务（开机自启、常驻自连）
  --download-only          只下载/准备二进制，不上线
  --skip-download          跳过下载，直接用安装目录已有二进制上线
  -h, --help               显示本帮助
USAGE
}

# ---- 解析参数 ----
while [ $# -gt 0 ]; do
    case "$1" in
        --control-plane) CONTROL_PLANE="$2"; shift 2 ;;
        --token)         TOKEN="$2"; shift 2 ;;
        --name)          NODE_NAME="$2"; shift 2 ;;
        --binary)        BINARY="$2"; shift 2 ;;
        --download-url)  DOWNLOAD_URL="$2"; shift 2 ;;
        --install-dir)   INSTALL_DIR="$2"; shift 2 ;;
        --data-dir)      DATA_DIR="$2"; shift 2 ;;
        --grpc-port)     GRPC_PORT="$2"; shift 2 ;;
        --ws-port)       WS_PORT="$2"; shift 2 ;;
        --service)       INSTALL_SERVICE="1"; shift ;;
        --download-only) DOWNLOAD_ONLY="1"; shift ;;
        --skip-download) SKIP_DOWNLOAD="1"; shift ;;
        -h|--help)       usage; exit 0 ;;
        *) echo "未知参数: $1" >&2; usage; exit 1 ;;
    esac
done

if [ "$DOWNLOAD_ONLY" = "1" ] && [ "$SKIP_DOWNLOAD" = "1" ]; then
    echo "错误: --download-only 与 --skip-download 互斥" >&2; exit 1
fi

# 上线阶段才校验 CP/token（仅下载时无需）。
if [ "$DOWNLOAD_ONLY" != "1" ]; then
    if [ -z "$CONTROL_PLANE" ]; then
        echo "错误: 缺少 --control-plane（上线阶段必填）" >&2; usage; exit 1
    fi
    if [ -z "$TOKEN" ]; then
        echo "错误: 缺少 --token（enrollment token，上线阶段必填）" >&2; usage; exit 1
    fi
fi
[ -n "$DATA_DIR" ] || DATA_DIR="$INSTALL_DIR/data"

# ---- 探测平台 ----
detect_os() {
    os="$(uname -s)"
    case "$os" in
        Linux)  echo "linux" ;;
        Darwin) echo "darwin" ;;
        *) echo "错误: 不支持的操作系统 $os（请用 --binary 指定二进制）" >&2; exit 1 ;;
    esac
}
detect_arch() {
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) echo "错误: 不支持的架构 $arch（请用 --binary 指定二进制）" >&2; exit 1 ;;
    esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"
echo "[1/4] 平台: $OS/$ARCH，安装目录: $INSTALL_DIR，数据根: $DATA_DIR"

mkdir -p "$INSTALL_DIR" "$DATA_DIR"
BIN_PATH="$INSTALL_DIR/jianmanager-worker"

# has_complete_binary 判定指定路径是否为「完整」Worker 二进制：存在 + 非空 + 可执行。
# 命中即视为已就绪、可跳过下载（用户拍板③：当前目录已有完整二进制则跳过下载）。
has_complete_binary() {
    p="$1"
    [ -f "$p" ] && [ -s "$p" ] && [ -x "$p" ]
}

# ---- 下载阶段：取 Worker 二进制（安装目录已有完整二进制则跳过）----
echo "[2/4] 准备 Worker 二进制"
if [ "$SKIP_DOWNLOAD" = "1" ]; then
    if has_complete_binary "$BIN_PATH"; then
        echo "      --skip-download：已存在完整二进制，跳过下载 ($BIN_PATH)"
    else
        echo "错误: --skip-download 但安装目录无完整二进制: $BIN_PATH" >&2
        echo "      请先 --download-only 下载，或用 --binary 指向已有二进制。" >&2
        exit 1
    fi
elif [ -n "$BINARY" ]; then
    # 显式 --binary：若它本身就是目标路径且已完整，免拷贝。
    [ -f "$BINARY" ] || { echo "错误: --binary 指定的文件不存在: $BINARY" >&2; exit 1; }
    if [ "$BINARY" = "$BIN_PATH" ] && has_complete_binary "$BIN_PATH"; then
        echo "      --binary 即安装路径且已完整，跳过拷贝 ($BIN_PATH)"
    else
        echo "      拷贝本地二进制: $BINARY -> $BIN_PATH"
        cp -f "$BINARY" "$BIN_PATH"
    fi
elif has_complete_binary "$BIN_PATH"; then
    # 安装目录已有完整二进制（前一次 --download-only 或手动拷贝）→ 跳过下载。
    echo "      已存在完整二进制，跳过下载 ($BIN_PATH)"
elif [ -n "$DOWNLOAD_URL" ]; then
    url="$DOWNLOAD_URL"
    # ADR-036 产物命名契约: <base>/worker-<os>-<arch>（os=GOOS、arch=GOARCH）。
    # 若 --download-url 已指向具体产物文件（含 worker- 资产名）则原样用。
    case "$url" in
        */worker-*) : ;;
        */) url="${url}worker-${OS}-${ARCH}" ;;
        *) url="${url}/worker-${OS}-${ARCH}" ;;
    esac
    echo "      下载: $url"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$BIN_PATH"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$BIN_PATH" "$url"
    else
        echo "错误: 需要 curl 或 wget 下载二进制" >&2; exit 1
    fi
else
    echo "错误: 未提供 --binary 也未提供 --download-url，无法获取 Worker 二进制" >&2
    echo "      内网/离线请先拷贝二进制并用 --binary 指向它。" >&2
    exit 1
fi
chmod +x "$BIN_PATH"

if [ "$DOWNLOAD_ONLY" = "1" ]; then
    echo "[3/4] --download-only：二进制已就绪，跳过上线 ($BIN_PATH)"
    echo "[4/4] 完成。上线请再跑: $BIN_PATH --control-plane <cp:9100> --token <jmet_...> [--name N]"
    echo "      或经本脚本: install-worker.sh --skip-download --control-plane <cp> --token <jmet_...>"
    exit 0
fi

# ---- 上线阶段：调 worker setup（传参/env），由 Worker 自配 + 注册 + run ----
# Worker 免配置自启 setup（FR-222）：非 TTY 下从 --control-plane/--token/--name/--grpc-port/
# --ws-port/--data-dir + JIANMANAGER_* env 读，自己写 worker.yml + 注册 + 持久化身份 + 转 run。
# 脚本据此把入参喂给 worker，不再自己写 worker.yml。token 仅经 env 传、绝不落盘。
if [ "$INSTALL_SERVICE" = "1" ]; then
    if ! command -v systemctl >/dev/null 2>&1; then
        echo "错误: 系统无 systemd（systemctl 不存在），无法 --service 注册服务" >&2; exit 1
    fi
    echo "[3/4] 注册 systemd 服务 jianmanager-worker（ExecStart 跑 worker 自配 setup）"
    UNIT="/etc/systemd/system/jianmanager-worker.service"
    # 首次启动：worker 无配置 → 自启 setup，读环境里的 CP/token/节点名等完成写配置 + 注册 +
    # 持久化身份 + 转 run。注册成功后落本地身份文件，后续重启 worker 判「已配置」直接 run、不再用 token。
    # token 经服务进程环境注入（一次性），不写入任何配置文件。
    SVC_ENV="Environment=JIANMANAGER_CONTROL_PLANE=$CONTROL_PLANE
Environment=JIANMANAGER_ENROLL_TOKEN=$TOKEN
Environment=JIANMANAGER_GRPC_PORT=$GRPC_PORT
Environment=JIANMANAGER_WS_PORT=$WS_PORT
Environment=JIANMANAGER_DATA_DIR=$DATA_DIR"
    [ -z "$NODE_NAME" ] || SVC_ENV="$SVC_ENV
Environment=JIANMANAGER_NODE_NAME=$NODE_NAME"
    cat > "$UNIT" <<UNIT_EOF
[Unit]
Description=JianManager Worker Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$BIN_PATH
$SVC_ENV
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT_EOF
    systemctl daemon-reload
    systemctl enable jianmanager-worker >/dev/null 2>&1 || true
    systemctl restart jianmanager-worker
    echo "[4/4] 完成。服务已启动（首次自配上线），查看日志: journalctl -u jianmanager-worker -f"
else
    echo "[3/4] 未指定 --service，前台调 worker 自配上线（Ctrl+C 退出；生产建议加 --service）"
    echo "[4/4] 启动 Worker（首次自配 setup）..."
    # 前台上线：CP/节点名/端口经 flag 传，token 仅经 env（不出现在进程命令行参数里）。
    set -- --control-plane "$CONTROL_PLANE" --grpc-port "$GRPC_PORT" --ws-port "$WS_PORT" --data-dir "$DATA_DIR"
    [ -z "$NODE_NAME" ] || set -- "$@" --name "$NODE_NAME"
    cd "$INSTALL_DIR"
    JIANMANAGER_ENROLL_TOKEN="$TOKEN" exec "$BIN_PATH" "$@"
fi
