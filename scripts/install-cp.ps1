# JianManager Control Plane 一键下载（Windows PowerShell）。
# 见 FR-355：从 GitHub Releases 拉取 control-plane-windows-amd64.exe；不做服务化。
# 用法:
#   irm https://raw.githubusercontent.com/wcpe/JianManager/dev/scripts/install-cp.ps1 | iex
#   .\install-cp.ps1 -InstallDir C:\jianmanager -Start

[CmdletBinding()]
param(
    [string]$InstallDir = ".",
    [string]$DownloadUrl = $(if ($env:JIANMANAGER_CP_DOWNLOAD_URL) { $env:JIANMANAGER_CP_DOWNLOAD_URL } else { "https://github.com/wcpe/JianManager/releases/latest/download" }),
    # full=内嵌 Worker；slim=不内嵌 Worker（探针仍内嵌）。也可用环境变量 JIANMANAGER_CP_VARIANT。
    [ValidateSet("full", "slim")]
    [string]$Variant = $(if ($env:JIANMANAGER_CP_VARIANT -eq "slim") { "slim" } else { "full" }),
    [string]$Binary = "",
    [switch]$SkipDownload,
    [switch]$Start
)

$ErrorActionPreference = "Stop"

function Write-ErrCn([string]$msg) {
    Write-Error "错误: $msg"
}

$asset = if ($Variant -eq "slim") { "control-plane-slim-windows-amd64.exe" } else { "control-plane-windows-amd64.exe" }
$InstallDir = (New-Item -ItemType Directory -Force -Path $InstallDir).FullName
$target = Join-Path $InstallDir "control-plane.exe"

if ($Binary) {
    if (-not (Test-Path -LiteralPath $Binary)) { Write-ErrCn "本地二进制不存在: $Binary" }
    Copy-Item -LiteralPath $Binary -Destination $target -Force
    Write-Host "已复制本地二进制 → $target"
}
elseif ($SkipDownload) {
    if (-not (Test-Path -LiteralPath $target)) { Write-ErrCn "安装目录无二进制: $target" }
    Write-Host "跳过下载，使用已有 $target"
}
else {
    $url = "$($DownloadUrl.TrimEnd('/'))/$asset"
    Write-Host "下载 Control Plane: $url"
    $tmp = "$target.tmp"
    try {
        Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
    }
    catch {
        if (Test-Path $tmp) { Remove-Item $tmp -Force }
        Write-ErrCn "下载失败（网络错误或 Release 无资产 $asset）。可改用 -DownloadUrl 或 -Binary。详情: $_"
    }
    $len = (Get-Item $tmp).Length
    if ($len -lt 1000000) {
        Remove-Item $tmp -Force
        Write-ErrCn "下载产物异常（仅 $len 字节），可能无匹配资产或被拦截"
    }
    Move-Item -LiteralPath $tmp -Destination $target -Force
    Write-Host "已安装 → $target"
}

Write-Host ""
Write-Host "安装完成。"
Write-Host "  启动: $target"
Write-Host "  浏览器: http://localhost:8080 （首次进入引导创建管理员）"
Write-Host "  生产请设置环境变量 JIANMANAGER_JWT_SECRET"
Write-Host "  完整配置见 docs/DEPLOY.md"
Write-Host ""

if ($Start) {
    Write-Host "前台启动 Control Plane…"
    & $target
}
