# FR-278: CP 内嵌 Worker 二进制（一键安装/升级不依赖 GitHub）

> 状态：🔨 开发中　·　关联 PRD：FR-278　·　关联 ADR：ADR-062（修订 ADR-059）

## 1. 背景与目标

真机复现（2026-07-11，一键安装 test 节点）：CP 缓存未命中 → 按 ADR-059 去 GitHub 拉取 → `下载 checksums.txt 失败: TLS handshake timeout`；且本地构建的 0.14.0 在 GitHub 无对应 release，**网络通也拉不到**。目标：安装/升级「与 CP 同版本 Worker」这一主链路**全程不出网**。

方案（ADR-062）：构建期把 windows/linux amd64 Worker go:embed 进 CP；`EnsureWorkerAsset` 解析顺序改为 **本地缓存（有效）> 内嵌物化 > 远程 feed**；内嵌命中即物化进 ADR-059 缓存目录，其余链路（token/下载端点/安装脚本/UpgradeNode/预缓存按钮）零改动复用。

## 2. 范围

### 做
- 嵌入目录 `internal/controlplane/embed/worker/`（`.gitkeep` 入库占位；二进制与 `manifest.json` 不入库）。
- 嵌入访问层：读 manifest（version/os/arch/sha256/size）+ 按平台取字节；空目录（未嵌）返回明确「未内嵌」。
- `EnsureWorkerAsset` 内嵌物化分支：请求版本 == `version.Version` 且 manifest 含该 os/arch → 写缓存目录（二进制 + 元数据 `sourceUrl=embedded://`）→ 复用既有校验/下发。
- 构建：`make embed-worker`（交叉编两平台 worker + 生成 manifest + 放入嵌入目录）；`make dist` 接入两阶段（embed-worker 先于 CP 编译）；CI release.yml build job 同步两阶段。
- CP 启动日志打印内嵌 Worker 状态（版本 + 平台清单 / 未内嵌）。

### 不做
- arm64 等更多平台嵌入（远程 feed 兜底路径覆盖）；多历史版本内嵌（版本锚定不变）；`make build`（本机开发构建）不强制嵌入。

## 3. 接口契约

无新 endpoint。行为变化：`GET /worker-assets/:version/:os/:arch/worker` 与 `POST /self-update/worker-assets/cache` 在「版本=CP 自身且平台已内嵌」时不再出网；缓存元数据 `sourceUrl` 新增 `embedded://cp-binary` 取值（系统更新页缓存列表可见来源）。错误语义不变（未嵌+缓存无+远程失败仍按现状报错）。

## 4. 验收标准

- [ ] `make dist` 产出的 CP 内嵌两平台 Worker（启动日志可证；CP 体积增 ~43MB）
- [ ] fresh checkout 未跑 `embed-worker` 时 `go build ./...` 照常通过；运行时资产状态为「未内嵌」、行为回退现状
- [ ] 单测：内嵌命中物化（sha256 与 manifest 一致、元数据 `embedded://`）/ 版本不匹配跳过内嵌 / 未嵌平台走远程 / 空嵌目录优雅降级
- [ ] 真机：断 GitHub（或直接无外网）+ 空缓存 → 一键安装 Windows 节点成功（下载来自内嵌物化，不出网）
- [ ] 系统更新页 Worker 缓存列表显示 `embedded://` 来源条目
