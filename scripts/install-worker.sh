#!/bin/sh
# JianManager Worker Node 一键安装脚本（Linux/macOS，POSIX sh）。
# 见 FR-080 / ADR-020：下载/拷贝 Worker 二进制 → 写 worker.yml → 以 enrollment token 首注册 →
# 可选注册 systemd 系统服务，使节点开机自启、常驻自连。脚本幂等：重复执行覆盖配置、重启服务。
#
# 一键命令形态（面板「添加节点」生成、直接粘贴）：
#   curl -fsSL <cp>/install-worker.sh | sh -s -- --control-plane <cp-grpc> --token <jmet_...> [--name N] [--service]
#
# enrollment token 一次性、限时；仅经命令行/环境变量传入，绝不写入 worker.yml。
# 注册成功后 CP 换发的 node_uuid/node_secret 由 Worker 持久化到 <data-dir>/etc/node-identity.json。
set -eu

# ---- 默认值 ----
CONTROL_PLANE=""        # CP gRPC 地址 host:port（必填）
TOKEN=""                # enrollment token 明文（必填）
NODE_NAME=""            # 节点名（可选，留空由 Worker 上报名生效）
BINARY=""               # 本地已拷贝的 Worker 二进制路径（离线/内网兜底，跳过下载）
# Worker 二进制下载地址（可选）。缺省走 GitHub Releases latest（ADR-036 产物命名契约：
# worker-<os>-<arch>[.exe]）；离线/内网用 --binary 或 --download-url 覆盖。
DOWNLOAD_URL="https://github.com/wcpe/jianmanager/releases/latest/download"
INSTALL_DIR="/opt/jianmanager"   # 安装目录
DATA_DIR=""             # 数据根（缺省 <install-dir>/data）
GRPC_PORT="9101"        # Worker gRPC 端口
WS_PORT="9102"          # Worker WS 终端端口
INSTALL_SERVICE="0"     # 是否注册 systemd 服务

usage() {
    cat <<'USAGE'
用法: install-worker.sh --control-plane <host:port> --token <jmet_...> [选项]

必填:
  --control-plane <addr>   Control Plane gRPC 地址 host:port
  --token <jmet_...>       一次性 enrollment token（面板「添加节点」生成）

可选:
  --name <node>            节点名（留空由 Worker 上报名生效）
  --binary <path>          本地 Worker 二进制路径（离线/内网，跳过下载）
  --download-url <url>     Worker 二进制下载基址/地址（默认 GitHub Releases latest）
  --install-dir <dir>      安装目录（默认 /opt/jianmanager）
  --data-dir <dir>         数据根目录（默认 <install-dir>/data）
  --grpc-port <port>       Worker gRPC 端口（默认 9101）
  --ws-port <port>         Worker WS 端口（默认 9102）
  --service                注册 systemd 服务（开机自启、常驻自连）
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
        -h|--help)       usage; exit 0 ;;
        *) echo "未知参数: $1" >&2; usage; exit 1 ;;
    esac
done

if [ -z "$CONTROL_PLANE" ]; then
    echo "错误: 缺少 --control-plane" >&2; usage; exit 1
fi
if [ -z "$TOKEN" ]; then
    echo "错误: 缺少 --token（enrollment token）" >&2; usage; exit 1
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
echo "[1/5] 平台: $OS/$ARCH，安装目录: $INSTALL_DIR，数据根: $DATA_DIR"

mkdir -p "$INSTALL_DIR" "$DATA_DIR"
BIN_PATH="$INSTALL_DIR/jianmanager-worker"

# ---- 获取二进制：优先本地 --binary，否则按 --download-url 下载 ----
echo "[2/5] 准备 Worker 二进制"
if [ -n "$BINARY" ]; then
    [ -f "$BINARY" ] || { echo "错误: --binary 指定的文件不存在: $BINARY" >&2; exit 1; }
    cp -f "$BINARY" "$BIN_PATH"
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

# ---- 写 worker.yml（enrollment token 不落盘）----
echo "[3/5] 写配置 $INSTALL_DIR/worker.yml"
{
    echo "# 由 install-worker.sh 生成（FR-080）。enrollment token 不写入本文件。"
    echo "name: ${NODE_NAME:-node-$(hostname 2>/dev/null || echo local)}"
    echo "control_plane: $CONTROL_PLANE"
    echo "data_dir: $DATA_DIR"
    echo "grpc:"
    echo "  port: $GRPC_PORT"
    echo "ws:"
    echo "  port: $WS_PORT"
    echo "log:"
    echo "  level: info"
    echo "  format: json"
} > "$INSTALL_DIR/worker.yml"

# ---- 注册系统服务 或 前台启动完成首注册 ----
if [ "$INSTALL_SERVICE" = "1" ]; then
    if ! command -v systemctl >/dev/null 2>&1; then
        echo "错误: 系统无 systemd（systemctl 不存在），无法 --service 注册服务" >&2; exit 1
    fi
    echo "[4/5] 注册 systemd 服务 jianmanager-worker"
    UNIT="/etc/systemd/system/jianmanager-worker.service"
    # enrollment token 经服务环境注入首注册；注册成功后落本地身份文件，后续重启不再依赖它。
    cat > "$UNIT" <<UNIT_EOF
[Unit]
Description=JianManager Worker Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$BIN_PATH $INSTALL_DIR/worker.yml
Environment=JIANMANAGER_ENROLL_TOKEN=$TOKEN
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT_EOF
    systemctl daemon-reload
    systemctl enable jianmanager-worker >/dev/null 2>&1 || true
    systemctl restart jianmanager-worker
    echo "[5/5] 完成。服务已启动，查看日志: journalctl -u jianmanager-worker -f"
else
    echo "[4/5] 未指定 --service，前台启动完成首次注册（Ctrl+C 退出；生产建议加 --service）"
    echo "[5/5] 启动 Worker..."
    JIANMANAGER_ENROLL_TOKEN="$TOKEN" exec "$BIN_PATH" "$INSTALL_DIR/worker.yml"
fi
