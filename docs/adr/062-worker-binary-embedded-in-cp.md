# ADR-062: Worker 二进制内嵌进 CP 作为首选分发源

- **日期**: 2026-07-11
- **状态**: accepted（关联 FR-278；修订 ADR-059）
- **关联**: FR-278、FR-190、ADR-036、ADR-059

## 上下文

ADR-059 把 CP 定为 Worker 二进制的代理缓存与 LAN 分发源，但**取得来源仍是 GitHub Releases**（缓存未命中时 CP 出站即时拉取）。真机部署暴露两个致命场景：

1. **受限网络**：国内/内网环境访问 GitHub TLS 握手常年超时，即使配了出站代理也不稳；一键安装在缓存未命中时必然 502。
2. **本地构建版本远程必然不存在**：`make dist` 出的开发版 CP（如 tag 未 push 的 0.14.0），GitHub 上没有对应 release——**网络再通也拉不到**，安装/升级链路结构性死锁。

而 Worker 与 CP 同仓同构建（纯 Go、交叉编译零成本），「与 CP 自身版本一致的 Worker」在构建期就已存在——舍近求远去公网拉自己，是架构上的绕路。

## 决策

**构建期把 windows/linux amd64 两平台 Worker 二进制 go:embed 进 CP，作为 `worker-assets` 的首选来源；本地缓存与远程 feed 降级为后备。**

1. **嵌入内容**：`internal/controlplane/embed/worker/` 内嵌 `worker-windows-amd64.exe`、`worker-linux-amd64` 与 `manifest.json`（version/os/arch/sha256/size，构建期生成）。目录以 `.gitkeep` 占位入库、二进制不入库——fresh checkout 未跑嵌入目标时 CP 照常编译，运行时优雅降级（与探针/客户端更新器的可选内嵌模式一致）。
2. **构建两阶段**：`make dist` 与 CI release 管线先交叉编译两平台 Worker → 生成 manifest 并放入嵌入目录 → 再编译 CP。CP 体积 +~43MB（可接受；换来分发自足）。
3. **解析顺序**：`EnsureWorkerAsset` 改为 **本地缓存（有效）> 内嵌物化 > 远程 feed**。内嵌命中条件 = 请求版本与 CP `version.Version` 一致、且内嵌 manifest 含该 os/arch；命中即把内嵌字节物化到 ADR-059 的缓存目录（`cache/worker-assets/<version>/<os>-<arch>/`），此后完全复用既有缓存校验/下发/审计链路。下载端点、安装脚本、UpgradeNode、系统更新页预缓存按钮**零改动**、自动受益。
4. **版本锚定不变**：内嵌只服务「与 CP 自身版本一致」的请求（天然满足——嵌的就是同构建产物）；跨版本请求仍走缓存/远程，ADR-059 的锚定语义保持。
5. **信任模型**：内嵌资产与 CP 同一二进制、同一信任边界，无需 checksums.txt 链；物化时按构建期 manifest 的 sha256 写缓存元数据并校验，缓存层完整性语义与 ADR-059 一致。

## 理由

- **为何内嵌而非仅手动上传/预缓存**：ADR-059 替代方案里的「手动上传」体验差且易错版本；预缓存按钮在断外网 CP 上同样死（它也走远程）。内嵌让「安装/升级与 CP 同版本 Worker」这一**主场景**零依赖、零操作可用。
- **为何物化进缓存而非直接从 embed.FS 下发**：物化后 Open/校验/ServeContent/审计全复用 ADR-059 既有实现，改动面收敛在 `EnsureWorkerAsset` 一处；embed.FS 文件不支持 Seek，直接下发还需另写 serve 路径。
- **为何缓存优先于内嵌**：缓存可能承载运维手动放置的热修 Worker（同版本重打包）；缓存有效即尊重之，内嵌只在缺失时补位。语义与「内嵌探针 vs 数据根缓存」一致。
- **为何仍保留远程 feed**：跨版本资产（理论上不出现于安装/升级主链路）与未嵌平台（如未来 arm64 先于嵌入支持）仍需远程兜底；保留不增加成本。

## 后果

- `make dist` 目标重排为两阶段；新增 `embed-worker`（可选目标，本地开发 `make build` 不强制）。CI release.yml build job 在编 CP 前先出两平台 Worker 并嵌入。
- `SelfUpdateService.EnsureWorkerAsset` 增加内嵌物化分支 + 单测（命中物化 / 版本不匹配跳过 / 未嵌平台降级远程 / fresh checkout 空嵌降级）。
- CP 启动日志打印内嵌 Worker 资产状态（版本/平台清单或「未内嵌」），便于排障。
- 系统更新页 Worker 缓存状态可显示来源（内嵌物化 / 远程拉取 / 手动放置，经缓存元数据 `sourceUrl` 区分：`embedded://`、`manual://`、https）。
- 发布产物仍上传独立 worker 二进制（外部脚本 `--binary` 兜底路径保留）。

## 对 ADR-059 的修订

ADR-059 的缓存目录、token 下发端点、安装脚本、UpgradeNode、版本锚定、审计语义**全部保留**；仅其「可信来源 = GitHub Releases/feed」一条被本 ADR 修订为「内嵌（首选，同版本）+ 远程 feed（兜底）」。ADR-059 状态行标注被本 ADR 修订，不整体取代。
