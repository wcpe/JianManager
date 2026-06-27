# JianManager Worker Node 一键安装脚本（Windows PowerShell）。
# 见 FR-080 / ADR-020：下载/拷贝 Worker 二进制 → 写 worker.yaml → 以 enrollment token 首注册 →
# 可选注册 Windows 服务，使节点开机自启、常驻自连。脚本幂等：重复执行覆盖配置、重建服务。
#
# 一键命令形态（面板「添加节点」生成、直接粘贴到 PowerShell）：
#   iwr <cp>/install-worker.ps1 -UseBasicParsing | iex; Install-JianManagerWorker -ControlPlane <cp-grpc> -Token <jmet_...> [-Name N] [-Service]
#
# enrollment token 一次性、限时；仅经参数/环境变量传入，绝不写入 worker.yaml。
# 注册成功后 CP 换发的 node_uuid/node_secret 由 Worker 持久化到 <DataDir>\etc\node-identity.json。

function Install-JianManagerWorker {
    [CmdletBinding()]
    param(
        # Control Plane gRPC 地址 host:port（必填）。
        [Parameter(Mandatory = $true)][string]$ControlPlane,
        # 一次性 enrollment token 明文（必填，面板「添加节点」生成）。
        [Parameter(Mandatory = $true)][string]$Token,
        # 节点名（可选，留空由 Worker 上报名生效）。
        [string]$Name = "",
        # 本地已拷贝的 Worker 二进制路径（离线/内网兜底，跳过下载）。
        [string]$Binary = "",
        # Worker 二进制下载基址/地址（可选）。缺省走 GitHub Releases latest
        # （ADR-036 产物命名契约：worker-<os>-<arch>.exe）；离线/内网用 -Binary 或 -DownloadUrl 覆盖。
        [string]$DownloadUrl = "https://github.com/wcpe/jianmanager/releases/latest/download",
        # 安装目录（默认 C:\JianManager）。
        [string]$InstallDir = "C:\JianManager",
        # 数据根目录（默认 <InstallDir>\data）。
        [string]$DataDir = "",
        # Worker gRPC 端口（默认 9101）。
        [int]$GrpcPort = 9101,
        # Worker WS 终端端口（默认 9102）。
        [int]$WsPort = 9102,
        # 注册 Windows 服务（开机自启、常驻自连）。
        [switch]$Service
    )

    $ErrorActionPreference = "Stop"
    if (-not $DataDir) { $DataDir = Join-Path $InstallDir "data" }

    # 探测架构（Worker 仅 amd64/arm64）。
    $arch = switch ($env:PROCESSOR_ARCHITECTURE) {
        "AMD64" { "amd64" }
        "ARM64" { "arm64" }
        default { throw "不支持的架构 $($env:PROCESSOR_ARCHITECTURE)（请用 -Binary 指定二进制）" }
    }
    Write-Host "[1/5] 平台: windows/$arch，安装目录: $InstallDir，数据根: $DataDir"

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
    $binPath = Join-Path $InstallDir "jianmanager-worker.exe"

    # 获取二进制：优先 -Binary，否则按 -DownloadUrl 下载。
    Write-Host "[2/5] 准备 Worker 二进制"
    if ($Binary) {
        if (-not (Test-Path $Binary)) { throw "-Binary 指定的文件不存在: $Binary" }
        Copy-Item -Force -Path $Binary -Destination $binPath
    } elseif ($DownloadUrl) {
        $url = $DownloadUrl
        # ADR-036 产物命名契约: <base>/worker-windows-<arch>.exe；若已指向具体产物文件则原样用。
        if ($url -notlike "*worker-*") {
            $url = $url.TrimEnd("/") + "/worker-windows-$arch.exe"
        }
        Write-Host "      下载: $url"
        Invoke-WebRequest -Uri $url -OutFile $binPath -UseBasicParsing
    } else {
        throw "未提供 -Binary 也未提供 -DownloadUrl，无法获取 Worker 二进制（内网/离线请先拷贝二进制并用 -Binary 指向它）"
    }

    # 写 worker.yaml（enrollment token 不落盘）。
    $cfgPath = Join-Path $InstallDir "worker.yaml"
    Write-Host "[3/5] 写配置 $cfgPath"
    if (-not $Name) { $Name = "node-$($env:COMPUTERNAME)" }
    # YAML 路径用正斜杠，避免反斜杠转义歧义。
    $dataDirYaml = $DataDir -replace '\\', '/'
    $cfg = @"
# 由 install-worker.ps1 生成（FR-080）。enrollment token 不写入本文件。
name: $Name
control_plane: $ControlPlane
data_dir: $dataDirYaml
grpc:
  port: $GrpcPort
ws:
  port: $WsPort
log:
  level: info
  format: json
"@
    Set-Content -Path $cfgPath -Value $cfg -Encoding UTF8

    if ($Service) {
        # 注册 Windows 服务：enrollment token 经服务环境注入首注册；注册成功后落本地身份文件，
        # 后续重启不再依赖它。服务进程级环境变量经注册表写入（HKLM 服务 Environment 多字符串值）。
        Write-Host "[4/5] 注册 Windows 服务 JianManagerWorker"
        $svcName = "JianManagerWorker"
        $binArgs = "`"$binPath`" `"$cfgPath`""
        if (Get-Service -Name $svcName -ErrorAction SilentlyContinue) {
            Stop-Service -Name $svcName -Force -ErrorAction SilentlyContinue
            sc.exe delete $svcName | Out-Null
            Start-Sleep -Seconds 1
        }
        New-Service -Name $svcName -BinaryPathName $binArgs -DisplayName "JianManager Worker Node" -StartupType Automatic | Out-Null
        # 服务进程环境注入 enrollment token（首注册用）。
        $svcKey = "HKLM:\SYSTEM\CurrentControlSet\Services\$svcName"
        Set-ItemProperty -Path $svcKey -Name "Environment" -Value @("JIANMANAGER_ENROLL_TOKEN=$Token") -Type MultiString
        Start-Service -Name $svcName
        Write-Host "[5/5] 完成。服务已启动，查看状态: Get-Service $svcName"
    } else {
        Write-Host "[4/5] 未指定 -Service，前台启动完成首次注册（Ctrl+C 退出；生产建议加 -Service）"
        Write-Host "[5/5] 启动 Worker..."
        $env:JIANMANAGER_ENROLL_TOKEN = $Token
        & $binPath $cfgPath
    }
}

# 若脚本被直接执行（非经 iex 引入后调函数），且带了参数，则透传给函数。
if ($MyInvocation.InvocationName -ne '.' -and $args.Count -gt 0) {
    Install-JianManagerWorker @args
}
