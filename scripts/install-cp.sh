#!/bin/sh
# JianManager Control Plane 一键下载安装脚本（Linux/macOS，POSIX sh）。
# 见 FR-355：下载 GitHub Releases 产物 + 可执行权限 + 启动提示；不做 systemd 服务化。
#
# 用法示例：
#   curl -fsSL https://raw.githubusercontent.com/wcpe/JianManager/dev/scripts/install-cp.sh | sh
#   sh install-cp.sh --install-dir /opt/jianmanager --start
#   sh install-cp.sh --variant slim
set -eu

REPO_DEFAULT="wcpe/JianManager"
DOWNLOAD_URL="${JIANMANAGER_CP_DOWNLOAD_URL:-https://github.com/${REPO_DEFAULT}/releases/latest/download}"
INSTALL_DIR="."
BINARY=""
SKIP_DOWNLOAD="0"
DO_START="0"
# 产物档位：full=内嵌 Worker 的完整 CP；slim=不内嵌 Worker（探针仍内嵌），体积更小。
# 也可用环境变量 JIANMANAGER_CP_VARIANT=slim|full。
VARIANT="${JIANMANAGER_CP_VARIANT:-full}"

usage() {
    cat <<'USAGE'
用法: install-cp.sh [选项]

可选:
  --install-dir <dir>   安装目录（默认当前目录 .）
  --download-url <url>  下载基址（默认 GitHub Releases latest；可指向镜像）
  --variant <full|slim> 产物档位（默认 full；slim=不内嵌 Worker，探针仍内嵌）
  --binary <path>       使用本地已有二进制（跳过下载）
  --skip-download       跳过下载，使用安装目录内 control-plane
  --start               安装后前台启动（默认只打印启动命令）
  -h, --help            显示本帮助

环境变量:
  JIANMANAGER_CP_DOWNLOAD_URL  覆盖默认下载基址（镜像）
  JIANMANAGER_CP_VARIANT       full|slim（同 --variant）
USAGE
}

die() {
    echo "错误: $*" >&2
    exit 1
}

# 资产文件名：
#   full → control-plane-<os>-<arch>[.exe]
#   slim → control-plane-slim-<os>-<arch>[.exe]
# INSTALL_CP_TEST=1 时仅打印映射表自检结果。
asset_name() {
    os="$1"
    arch="$2"
    variant="$3"
    prefix="control-plane"
    if [ "$variant" = "slim" ]; then
        prefix="control-plane-slim"
    fi
    case "$os" in
        linux|darwin) ;;
        windows) echo "${prefix}-windows-${arch}.exe"; return ;;
        *) die "不支持的操作系统: $os（支持 linux/darwin/windows）" ;;
    esac
    case "$arch" in
        amd64|arm64) ;;
        *) die "不支持的架构: $arch（支持 amd64/arm64）" ;;
    esac
    echo "${prefix}-${os}-${arch}"
}

detect_os() {
    u=$(uname -s 2>/dev/null || echo unknown)
    case "$u" in
        Linux*) echo linux ;;
        Darwin*) echo darwin ;;
        MINGW*|MSYS*|CYGWIN*) echo windows ;;
        *) die "无法识别操作系统 uname=$u" ;;
    esac
}

detect_arch() {
    m=$(uname -m 2>/dev/null || echo unknown)
    case "$m" in
        x86_64|amd64) echo amd64 ;;
        aarch64|arm64) echo arm64 ;;
        *) die "无法识别 CPU 架构 uname -m=$m" ;;
    esac
}

if [ "${INSTALL_CP_TEST:-}" = "1" ]; then
    fail=0
    check() {
        got=$(asset_name "$1" "$2" "$3")
        if [ "$got" != "$4" ]; then
            echo "FAIL asset_name($1,$2,$3)=${got} want $4" >&2
            fail=1
        else
            echo "OK   $4"
        fi
    }
    check linux amd64 full control-plane-linux-amd64
    check linux amd64 slim control-plane-slim-linux-amd64
    check linux arm64 full control-plane-linux-arm64
    check darwin arm64 full control-plane-darwin-arm64
    check windows amd64 full control-plane-windows-amd64.exe
    check windows amd64 slim control-plane-slim-windows-amd64.exe
    [ "$fail" -eq 0 ] || exit 1
    exit 0
fi

while [ $# -gt 0 ]; do
    case "$1" in
        --install-dir) INSTALL_DIR="$2"; shift 2 ;;
        --download-url) DOWNLOAD_URL="$2"; shift 2 ;;
        --variant) VARIANT="$2"; shift 2 ;;
        --binary) BINARY="$2"; shift 2 ;;
        --skip-download) SKIP_DOWNLOAD="1"; shift ;;
        --start) DO_START="1"; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die "未知参数: $1（--help 查看用法）" ;;
    esac
done

case "$VARIANT" in
    full|slim) : ;;
    *) die "--variant 仅支持 full|slim（收到: $VARIANT）" ;;
esac

OS=$(detect_os)
ARCH=$(detect_arch)
ASSET=$(asset_name "$OS" "$ARCH" "$VARIANT")

mkdir -p "$INSTALL_DIR"
INSTALL_DIR=$(CDPATH= cd -- "$INSTALL_DIR" && pwd)
TARGET="$INSTALL_DIR/control-plane"
if [ "$OS" = "windows" ]; then
    TARGET="$INSTALL_DIR/control-plane.exe"
fi

if [ -n "$BINARY" ]; then
    [ -f "$BINARY" ] || die "本地二进制不存在: $BINARY"
    cp "$BINARY" "$TARGET"
    echo "已复制本地二进制 → $TARGET"
elif [ "$SKIP_DOWNLOAD" = "1" ]; then
    [ -f "$TARGET" ] || die "安装目录无二进制: $TARGET（去掉 --skip-download 或指定 --binary）"
    echo "跳过下载，使用已有 $TARGET"
else
    URL="${DOWNLOAD_URL%/}/$ASSET"
    echo "下载 Control Plane ($VARIANT): $URL"
    tmp="${TARGET}.tmp.$$"
    if command -v curl >/dev/null 2>&1; then
        if ! curl -fL --retry 3 --connect-timeout 15 -o "$tmp" "$URL"; then
            rm -f "$tmp"
            die "下载失败（网络错误或 Release 无资产 $ASSET）。可改用 --download-url 或 --binary"
        fi
    elif command -v wget >/dev/null 2>&1; then
        if ! wget -O "$tmp" "$URL"; then
            rm -f "$tmp"
            die "下载失败（网络错误或 Release 无资产 $ASSET）。可改用 --download-url 或 --binary"
        fi
    else
        die "需要 curl 或 wget 以下载二进制"
    fi
    size=$(wc -c <"$tmp" | tr -d ' ')
    if [ "${size:-0}" -lt 1000000 ]; then
        rm -f "$tmp"
        die "下载产物异常（仅 ${size:-0} 字节），可能无匹配资产 $ASSET 或被拦截"
    fi
    mv "$tmp" "$TARGET"
    echo "已安装 → $TARGET"
fi

chmod +x "$TARGET" 2>/dev/null || true

echo ""
echo "安装完成。"
echo "  启动: $TARGET"
echo "  浏览器: http://<本机IP>:8080 （首次进入引导创建管理员）"
echo "  生产请设置: export JIANMANAGER_JWT_SECRET=\"\$(openssl rand -hex 32)\""
echo "  完整配置见 docs/DEPLOY.md"
echo ""

if [ "$DO_START" = "1" ]; then
    echo "前台启动 Control Plane…"
    exec "$TARGET"
fi
