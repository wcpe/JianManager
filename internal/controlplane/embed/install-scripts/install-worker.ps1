# JianManager Worker Node 安装脚本（Windows PowerShell）。
# 见 FR-223 / ADR-051（改写 ADR-020 的安装编排立场）：脚本退化为「取二进制 + 调 worker setup」。
#
# 两阶段（参考 GitHub Actions Runner「下载」与「config+run」分离）：
#   [下载阶段] 取 Worker 二进制。安装目录（或 -Binary 指向处）已有完整二进制则跳过下载。
#   [上线阶段] 调 `worker --control-plane ... --token ...`，由 Worker 自己写 worker.yml +
#              携 enrollment token 首注册 + 持久化身份 + 转入 run（worker 免配置自启 setup，FR-222）。
#              本脚本不再自己写 worker.yml（配置归属回到 Worker 自身）。
#   可选 -Service 注册 Windows 服务常驻：服务可执行直接跑 worker（首次 setup 写配置 + 持久化身份后，
#   服务重启复用持久化配置，不再依赖 token）。
#
# 阶段控制：
#   -DownloadOnly   只下载/准备二进制，不上线（下载与上线分两次执行时用）
#   -SkipDownload   跳过下载，直接用安装目录已有二进制上线
#   缺省            顺序两段：下载（按需）→ 上线
#
# 一键命令形态（面板「添加节点」生成、直接粘贴到 PowerShell）：
#   iwr <cp>/install-worker.ps1 -UseBasicParsing | iex; Install-JianManagerWorker -ControlPlane <cp-grpc> -Token <jmet_...> [-Name N] [-Service]
#
# enrollment token 一次性、限时；仅经参数/环境变量传入，绝不写入磁盘（worker setup 同样不落盘）。
# 注册成功后 CP 换发的 node_uuid/node_secret 由 Worker 持久化到 <DataDir>\etc\node-identity.json。

function Install-JianManagerWorker {
    [CmdletBinding()]
    param(
        # Control Plane gRPC 地址 host:port（上线阶段必填）。
        [string]$ControlPlane = "",
        # 一次性 enrollment token 明文（上线阶段必填，面板「添加节点」生成）。
        [string]$Token = "",
        # 节点名（可选，留空由 Worker/CP 预设名生效）。
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
        [switch]$Service,
        # 只下载/准备二进制，不上线。
        [switch]$DownloadOnly,
        # 跳过下载，直接用安装目录已有二进制上线。
        [switch]$SkipDownload
    )

    $ErrorActionPreference = "Stop"
    if (-not $DataDir) { $DataDir = Join-Path $InstallDir "data" }

    if ($DownloadOnly -and $SkipDownload) {
        throw "-DownloadOnly 与 -SkipDownload 互斥"
    }
    # 上线阶段才校验 CP/token（仅下载时无需）。
    if (-not $DownloadOnly) {
        if (-not $ControlPlane) { throw "缺少 -ControlPlane（上线阶段必填）" }
        if (-not $Token) { throw "缺少 -Token（enrollment token，上线阶段必填）" }
    }

    # 探测架构（Worker 仅 amd64/arm64）。
    $arch = switch ($env:PROCESSOR_ARCHITECTURE) {
        "AMD64" { "amd64" }
        "ARM64" { "arm64" }
        default { throw "不支持的架构 $($env:PROCESSOR_ARCHITECTURE)（请用 -Binary 指定二进制）" }
    }
    Write-Host "[1/4] 平台: windows/$arch，安装目录: $InstallDir，数据根: $DataDir"

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
    $binPath = Join-Path $InstallDir "jianmanager-worker.exe"

    # Test-CompleteBinary 判定路径是否为「完整」Worker 二进制：存在 + 非空。
    # 命中即视为已就绪、可跳过下载（用户拍板③：当前目录已有完整二进制则跳过下载）。
    function Test-CompleteBinary([string]$p) {
        return (Test-Path -LiteralPath $p -PathType Leaf) -and ((Get-Item -LiteralPath $p).Length -gt 0)
    }

    # 下载阶段：取 Worker 二进制（安装目录已有完整二进制则跳过）。
    Write-Host "[2/4] 准备 Worker 二进制"
    if ($SkipDownload) {
        if (Test-CompleteBinary $binPath) {
            Write-Host "      -SkipDownload：已存在完整二进制，跳过下载 ($binPath)"
        } else {
            throw "-SkipDownload 但安装目录无完整二进制: $binPath（请先 -DownloadOnly 下载，或用 -Binary 指向已有二进制）"
        }
    } elseif ($Binary) {
        # 显式 -Binary：若它本身就是目标路径且已完整，免拷贝。
        if (-not (Test-Path -LiteralPath $Binary)) { throw "-Binary 指定的文件不存在: $Binary" }
        # 规范化全路径比较（GetFullPath 不要求路径存在，避免对未生成的 $binPath 解析报错）。
        $binaryFull = [System.IO.Path]::GetFullPath($Binary)
        $binPathFull = [System.IO.Path]::GetFullPath($binPath)
        if (($binaryFull -eq $binPathFull) -and (Test-CompleteBinary $binPath)) {
            Write-Host "      -Binary 即安装路径且已完整，跳过拷贝 ($binPath)"
        } else {
            Write-Host "      拷贝本地二进制: $Binary -> $binPath"
            Copy-Item -Force -Path $Binary -Destination $binPath
        }
    } elseif (Test-CompleteBinary $binPath) {
        # 安装目录已有完整二进制（前一次 -DownloadOnly 或手动拷贝）→ 跳过下载。
        Write-Host "      已存在完整二进制，跳过下载 ($binPath)"
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

    if ($DownloadOnly) {
        Write-Host "[3/4] -DownloadOnly：二进制已就绪，跳过上线 ($binPath)"
        Write-Host "[4/4] 完成。上线请再跑: & '$binPath' --control-plane <cp:9100> --token <jmet_...> [--name N]"
        Write-Host "      或经本脚本: Install-JianManagerWorker -SkipDownload -ControlPlane <cp> -Token <jmet_...>"
        return
    }

    # 上线阶段：调 worker setup（传参/env），由 Worker 自配 + 注册 + run。
    # Worker 免配置自启 setup（FR-222）：非 TTY 下从 --control-plane/--token/--name/--grpc-port/
    # --ws-port/--data-dir + JIANMANAGER_* env 读，自己写 worker.yml + 注册 + 持久化身份 + 转 run。
    # 脚本据此把入参喂给 worker，不再自己写 worker.yml。token 仅经 env 传、绝不落盘。
    if ($Service) {
        # 注册 Windows 服务：服务 ImagePath 直接跑 worker（无配置 → 首次自启 setup）。CP/节点名/端口
        # 经服务进程级环境注入（HKLM 服务 Environment 多字符串值），首次 setup 据此写配置 + 注册 +
        # 持久化身份后，后续重启 worker 判「已配置」直接 run、不再用 token。token 一次性、仅注入服务环境。
        Write-Host "[3/4] 注册 Windows 服务 JianManagerWorker（ImagePath 跑 worker 自配 setup）"
        $svcName = "JianManagerWorker"
        # ImagePath 仅指向 worker.exe 本体（不带配置文件参数；worker 自配 setup 从环境读入参）。
        $binArgs = "`"$binPath`""
        if (Get-Service -Name $svcName -ErrorAction SilentlyContinue) {
            Stop-Service -Name $svcName -Force -ErrorAction SilentlyContinue
            sc.exe delete $svcName | Out-Null
            Start-Sleep -Seconds 1
        }
        New-Service -Name $svcName -BinaryPathName $binArgs -DisplayName "JianManager Worker Node" -StartupType Automatic | Out-Null
        # 服务进程环境注入上线入参（首次自配 setup 用；token 一次性）。
        $envEntries = @(
            "JIANMANAGER_CONTROL_PLANE=$ControlPlane",
            "JIANMANAGER_ENROLL_TOKEN=$Token",
            "JIANMANAGER_GRPC_PORT=$GrpcPort",
            "JIANMANAGER_WS_PORT=$WsPort",
            "JIANMANAGER_DATA_DIR=$DataDir"
        )
        if ($Name) { $envEntries += "JIANMANAGER_NODE_NAME=$Name" }
        $svcKey = "HKLM:\SYSTEM\CurrentControlSet\Services\$svcName"
        Set-ItemProperty -Path $svcKey -Name "Environment" -Value $envEntries -Type MultiString
        Start-Service -Name $svcName
        Write-Host "[4/4] 完成。服务已启动（首次自配上线），查看状态: Get-Service $svcName"
    } else {
        Write-Host "[3/4] 未指定 -Service，前台调 worker 自配上线（Ctrl+C 退出；生产建议加 -Service）"
        Write-Host "[4/4] 启动 Worker（首次自配 setup）..."
        # 前台上线：CP/节点名/端口经参数传，token 仅经 env（不出现在进程命令行参数里）。
        $wargs = @("--control-plane", $ControlPlane, "--grpc-port", "$GrpcPort", "--ws-port", "$WsPort", "--data-dir", $DataDir)
        if ($Name) { $wargs += @("--name", $Name) }
        $env:JIANMANAGER_ENROLL_TOKEN = $Token
        Push-Location $InstallDir
        try {
            & $binPath @wargs
        } finally {
            Pop-Location
            Remove-Item Env:JIANMANAGER_ENROLL_TOKEN -ErrorAction SilentlyContinue
        }
    }
}

# 若脚本被直接执行（非经 iex 引入后调函数），且带了参数，则透传给函数。
if ($MyInvocation.InvocationName -ne '.' -and $args.Count -gt 0) {
    Install-JianManagerWorker @args
}
