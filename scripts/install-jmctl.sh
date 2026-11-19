#!/bin/sh
# JianManager 紧急控制台 jmctl 一键下载安装脚本（Linux/macOS，POSIX sh）。
#
# 定位：CP 与 Worker 全部不可用时，在本机直连 daemon socket 做最后救援。
# 正因如此，它必须能被**独立获取**并**直接敲到命令名**——那时没有 CP 面板可点，
# 也不该要求现场装 Go 工具链去编译。
#
# 用法示例：
#   curl -fsSL https://raw.githubusercontent.com/wcpe/JianManager/dev/scripts/install-jmctl.sh | sh
#   sh install-jmctl.sh --install-dir /usr/local/bin
#   sh install-jmctl.sh --no-path        # 只装文件，不改 shell 配置
set -eu

REPO_DEFAULT="wcpe/JianManager"
DOWNLOAD_URL="${JIANMANAGER_JMCTL_DOWNLOAD_URL:-https://github.com/${REPO_DEFAULT}/releases/latest/download}"
# 默认装进用户级 bin：无需 root，且 ~/.local/bin 是 XDG 约定的用户命令目录。
INSTALL_DIR="${HOME}/.local/bin"
BINARY=""
SKIP_DOWNLOAD="0"
WRITE_PATH="1"

usage() {
    cat <<'USAGE'
用法: install-jmctl.sh [选项]

可选:
  --install-dir <dir>   安装目录（默认 ~/.local/bin）
  --download-url <url>  下载基址（默认 GitHub Releases latest；可指向镜像）
  --binary <path>       使用本地已有二进制（跳过下载）
  --skip-download       跳过下载，使用安装目录内已有的 jmctl
  --no-path             不修改 shell 配置（只装文件）
  -h, --help            显示本帮助

环境变量:
  JIANMANAGER_JMCTL_DOWNLOAD_URL  覆盖默认下载基址（镜像）
USAGE
}

die() {
    printf '错误: %s\n' "$1" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --install-dir) INSTALL_DIR="$2"; shift 2 ;;
        --download-url) DOWNLOAD_URL="$2"; shift 2 ;;
        --binary) BINARY="$2"; shift 2 ;;
        --skip-download) SKIP_DOWNLOAD="1"; shift ;;
        --no-path) WRITE_PATH="0"; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die "未知参数: $1（--help 查看用法）" ;;
    esac
done

detect_os() {
    case "$(uname -s)" in
        Linux) echo linux ;;
        Darwin) echo darwin ;;
        *) die "不支持的系统: $(uname -s)。Windows 请用 install-jmctl.ps1" ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo amd64 ;;
        *)
            # jmctl 走本机 daemon socket，实际部署只在 amd64 主机上；不做无依据的架构猜测。
            die "不支持的架构: $(uname -m)（当前仅发布 amd64）"
            ;;
    esac
}

OS=$(detect_os)
ARCH=$(detect_arch)
ASSET="jmctl-${OS}-${ARCH}"

mkdir -p "$INSTALL_DIR"
INSTALL_DIR=$(CDPATH= cd -- "$INSTALL_DIR" && pwd)
TARGET="$INSTALL_DIR/jmctl"

if [ -n "$BINARY" ]; then
    [ -f "$BINARY" ] || die "本地二进制不存在: $BINARY"
    cp "$BINARY" "$TARGET"
    echo "已复制本地二进制 → $TARGET"
elif [ "$SKIP_DOWNLOAD" = "1" ]; then
    [ -f "$TARGET" ] || die "安装目录无二进制: $TARGET（去掉 --skip-download 或指定 --binary）"
    echo "跳过下载，使用已有 $TARGET"
else
    URL="${DOWNLOAD_URL%/}/$ASSET"
    echo "下载 jmctl: $URL"
    tmp="${TARGET}.tmp.$$"
    if command -v curl >/dev/null 2>&1; then
        if ! curl -fL --retry 3 --connect-timeout 15 -o "$tmp" "$URL"; then
            rm -f "$tmp"
            die "下载失败（网络错误或 Release 无资产 $ASSET）。可改用 --download-url 或 --binary"
        fi
    elif command -v wget >/dev/null 2>&1; then
        if ! wget -O "$tmp" "$URL"; then
            rm -f "$tmp"
            die "下载失败（网络错误或 Release 无资产 $ASSET）"
        fi
    else
        die "需要 curl 或 wget 以下载二进制"
    fi
    # 兜底校验：Release 缺资产时 GitHub 会返回 HTML 页面，大小远小于真实产物。
    size=$(wc -c <"$tmp" | tr -d ' ')
    if [ "${size:-0}" -lt 1000000 ]; then
        rm -f "$tmp"
        die "下载产物异常（仅 ${size:-0} 字节），可能无匹配资产 $ASSET 或被拦截"
    fi
    mv "$tmp" "$TARGET"
    echo "已安装 → $TARGET"
fi

chmod +x "$TARGET" 2>/dev/null || true

# ── 注册到 PATH ──
#
# 只把目录写进 shell 配置，不做符号链接到 /usr/local/bin：
# 后者常需 root，且会在系统目录留下不受包管理器管理的文件。
# 判断「是否已在 PATH」用当前 shell 的实时值，避免重复追加。
already_in_path() {
    case ":${PATH}:" in
        *":$INSTALL_DIR:"*) return 0 ;;
        *) return 1 ;;
    esac
}

if [ "$WRITE_PATH" = "0" ]; then
    echo ""
    echo "已跳过 shell 配置修改（--no-path）。若 $INSTALL_DIR 不在 PATH 中，请自行加入。"
elif already_in_path; then
    echo ""
    echo "$INSTALL_DIR 已在 PATH 中，无需修改。"
else
    # 选择写入哪个 rc 文件：按登录 shell 判定，比猜更可靠。
    case "${SHELL:-}" in
        */zsh) RC="${ZDOTDIR:-$HOME}/.zshrc" ;;
        */bash)
            if [ -f "$HOME/.bashrc" ]; then RC="$HOME/.bashrc"; else RC="$HOME/.bash_profile"; fi
            ;;
        *) RC="$HOME/.profile" ;;
    esac
    LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
    if [ -f "$RC" ] && grep -Fqs "$LINE" "$RC"; then
        echo ""
        echo "$RC 中已存在 PATH 配置，未重复写入。"
    else
        {
            echo ""
            echo "# 由 JianManager jmctl 安装脚本添加"
            echo "$LINE"
        } >>"$RC"
        echo ""
        echo "已把 $INSTALL_DIR 写入 $RC"
        echo "当前 shell 需执行以下命令生效（或重开终端）："
        echo "  $LINE"
    fi
fi

echo ""
echo "安装完成。"
echo "  版本: $("$TARGET" --version 2>/dev/null || echo '(无法读取)')"
echo "  用法: jmctl list"
echo "  说明: 纯本机操作，直连 daemon socket，无网络面；详见 docs/DEPLOY.md"
echo ""
