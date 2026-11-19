# JianManager 紧急控制台 jmctl 一键下载安装脚本（Windows，PowerShell）。
#
# 定位：CP 与 Worker 全部不可用时，在本机直连 daemon 命名管道做最后救援。
# 正因如此，它必须能被独立获取并直接敲到命令名——那时没有 CP 面板可点，
# 也不该要求现场装 Go 工具链去编译。
#
# 用法示例：
#   irm https://raw.githubusercontent.com/wcpe/JianManager/dev/scripts/install-jmctl.ps1 | iex
#   .\install-jmctl.ps1 -InstallDir C:\Tools\jmctl
#   .\install-jmctl.ps1 -NoPath          # 只装文件，不改 PATH
[CmdletBinding()]
param(
    # 默认装进用户级目录：无需管理员权限，也不污染系统 Program Files。
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\jmctl"),
    [string]$DownloadUrl = $(if ($env:JIANMANAGER_JMCTL_DOWNLOAD_URL) { $env:JIANMANAGER_JMCTL_DOWNLOAD_URL } else { "https://github.com/wcpe/JianManager/releases/latest/download" }),
    [string]$Binary = "",
    [switch]$SkipDownload,
    [switch]$NoPath
)

$ErrorActionPreference = "Stop"

function Write-Err($msg) { Write-Host "错误: $msg" -ForegroundColor Red }

# 仅发布 amd64：jmctl 走本机 daemon 命名管道，实际部署都在 amd64 主机上。
if (-not [Environment]::Is64BitOperatingSystem) {
    Write-Err "不支持 32 位系统（当前仅发布 amd64）"
    exit 1
}

$asset = "jmctl-windows-amd64.exe"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$target = Join-Path $InstallDir "jmctl.exe"

if ($Binary) {
    if (-not (Test-Path $Binary)) { Write-Err "本地二进制不存在: $Binary"; exit 1 }
    Copy-Item -Force $Binary $target
    Write-Host "已复制本地二进制 → $target"
}
elseif ($SkipDownload) {
    if (-not (Test-Path $target)) { Write-Err "安装目录无二进制: $target（去掉 -SkipDownload 或指定 -Binary）"; exit 1 }
    Write-Host "跳过下载，使用已有 $target"
}
else {
    $url = "$($DownloadUrl.TrimEnd('/'))/$asset"
    Write-Host "下载 jmctl: $url"
    $tmp = "$target.tmp"
    try {
        # Invoke-WebRequest 无进度条时可显著提速；进度信息对脚本无意义。
        $prev = $ProgressPreference
        $ProgressPreference = "SilentlyContinue"
        Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
        $ProgressPreference = $prev
    }
    catch {
        if (Test-Path $tmp) { Remove-Item -Force $tmp }
        Write-Err "下载失败：$($_.Exception.Message)`n可改用 -DownloadUrl 指向镜像，或 -Binary 指定本地文件"
        exit 1
    }
    # 兜底校验：Release 缺资产时返回的是 HTML 页面，体积远小于真实产物。
    $size = (Get-Item $tmp).Length
    if ($size -lt 1000000) {
        Remove-Item -Force $tmp
        Write-Err "下载产物异常（仅 $size 字节），可能无匹配资产 $asset 或被拦截"
        exit 1
    }
    Move-Item -Force $tmp $target
    Write-Host "已安装 → $target"
}

# ── 注册到 PATH ──
#
# 写入**用户级** PATH（HKCU\Environment），不写系统级：
# 后者需管理员权限，且会影响机器上所有账户，对单个开发者工具过重。
# 同时更新当前会话的 $env:PATH，让用户不必重开终端就能用。
if ($NoPath) {
    Write-Host ""
    Write-Host "已跳过 PATH 修改（-NoPath）。若 $InstallDir 不在 PATH 中，请自行加入。"
}
else {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($null -eq $userPath) { $userPath = "" }
    $parts = $userPath -split ';' | Where-Object { $_ -ne "" }
    if ($parts -contains $InstallDir) {
        Write-Host ""
        Write-Host "$InstallDir 已在用户 PATH 中，无需修改。"
    }
    else {
        $newPath = if ($userPath.TrimEnd(';') -eq "") { $InstallDir } else { "$($userPath.TrimEnd(';'));$InstallDir" }
        [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
        Write-Host ""
        Write-Host "已把 $InstallDir 写入用户 PATH（注册表 HKCU\Environment）"
    }
    # 无论是否新写入，都同步当前会话——否则用户在同一条终端里立刻敲 jmctl 会找不到。
    if (($env:PATH -split ';') -notcontains $InstallDir) {
        $env:PATH = "$env:PATH;$InstallDir"
    }
}

$ver = try { & $target --version 2>$null } catch { "(无法读取)" }
Write-Host ""
Write-Host "安装完成。"
Write-Host "  版本: $ver"
Write-Host "  用法: jmctl list"
Write-Host "  说明: 纯本机操作，直连 daemon 命名管道，无网络面；详见 docs/DEPLOY.md"
Write-Host ""
