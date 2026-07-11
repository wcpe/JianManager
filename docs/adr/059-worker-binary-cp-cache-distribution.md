# ADR-059: Worker 二进制由 CP 代理缓存并经 LAN 下发

- **日期**: 2026-07-06
- **状态**: accepted（关联 FR-190；缓存/下载/UpgradeNode/系统更新页/安装命令已落地，真机内网验收待补）。「可信来源 = GitHub Releases/feed」一条被 ADR-062 修订为「内嵌（首选，同版本）+ 远程 feed（兜底）」，其余决策不变
- **关联**: FR-190、ADR-020、ADR-036、ADR-037、ADR-043、ADR-051、ADR-062

## 上下文

当前节点安装脚本与 Worker 升级链路默认让目标机器直接从公网 release 源下载 Worker 二进制。即使 ADR-037/043 已统一出站代理，实际部署仍有两个问题：

1. 内网节点不一定能访问 GitHub 或外网代理。
2. `latest` release 与当前 CP 版本可能错位，导致新节点或升级节点拿到与 CP 不匹配的 Worker。

FR-190 需要把 Worker 二进制取得路径收口到 CP：CP 使用自身出站代理获取与自身版本一致的 Worker 资产，校验后缓存，再在局域网内下发给安装脚本和节点升级流程。

## 决策

CP 成为 Worker 二进制的代理缓存与局域网分发源。

1. **版本锚定**：CP 只为“与自身 `version.Version` 一致”的 Worker 资产提供缓存和下发。开发版遵循当前 release 管线注入版本；无法解析对应资产时返回明确错误，不回退到远端 `latest`。
2. **可信来源**：CP 仍按 ADR-036 release 契约从 GitHub Releases 或配置的 feed 取得资产和 `checksums.txt`，下载经 ADR-037/043 的出站 HTTP client，校验 sha256 后才入缓存。
3. **缓存位置**：Worker 二进制缓存放在 CP 数据根 `cache/worker-assets/<version>/<os>-<arch>/`，保存二进制、sha256 和最小元数据。缓存是派生资产，可删除后重新拉取。
4. **LAN 下发端点**：新增只读端点供脚本和 Worker 升级下载缓存资产。端点必须带 CP 签发的短期 query token，不匿名暴露任意 Worker 二进制。升级 token 绑定 `version/os/arch/purpose/nodeUuid`；安装命令为跨平台脚本，安装 token 绑定 `version/purpose`，`os/arch` 可用通配符并由脚本运行时替换 URL 模板；默认 TTL 10 分钟。由于 token 位于 URL query，路由、审计、访问日志与错误响应必须脱敏或不记录 token 明文。
5. **安装脚本改造**：`GET /install-worker.sh` 与 `GET /install-worker.ps1` 继续由 CP 托管；添加节点一键命令默认签发 `purpose=install` 的 CP-local Worker 下载 URL 模板，脚本把 `{os}/{arch}` 替换为运行时平台，并识别 `/worker-assets/.../worker?token=...` 为完整下载地址，`--binary` 本地兜底和 `enroll.binary_url` 显式覆盖保留。
6. **UpgradeNode 改造**：CP 在调用 Worker `UpgradeWorker` 前先确保目标平台资产已缓存，再把 CP-local 下载 URL 与 sha256 下发给目标 Worker。Worker 无需访问公网 release 源。后端实现中 CP-local 基址由发起升级请求的 HTTP Host 推断，rollout 复用同一基址。
7. **不做多版本仓库**：本 FR 不实现任意历史版本下载、节点自行选择版本、P2P 分发或 CDN 能力。

## 理由

- CP 已是节点注册、升级编排和出站代理配置真相源；由 CP 缓存并下发能把“能否访问外网”从每个 Worker 收敛到一个位置。
- 版本锚定能避免 `latest` 与 CP 当前版本错位，降低控制面与节点协议不一致风险。
- 缓存作为派生资产放入 `cache/`，符合数据根 FHS 语义，清理后可重建。
- 继续使用 ADR-036 的 release 校验链，避免引入新的信任根。

## 后果

- `SelfUpdateService` 需要增加 Worker 资产缓存解析、下载、校验和缓存命中逻辑。
- 安装脚本的一键命令默认不再拼公网 `enroll.binary_url`，而是拼 CP-local Worker 下载 URL。
- Worker `UpgradeWorker` 仍只接收 URL + sha256，不需要知道远端源类型。
- 需要在 API/spec 中定义缓存状态、下载端点、错误码、token 校验和审计动作；审计只记录 token purpose、scope 摘要、签发/使用结果，不记录 token 明文。
- 真机验收必须覆盖“Worker 所在机器断外网但可访问 CP”时安装与升级成功。

## 替代方案

- **继续让 Worker 直连公网**：实现最少，但不能解决内网无公网和版本错位，放弃。
- **把所有历史 Worker 版本做成 CP 内建仓库**：能力更强，但存储、清理和版本选择复杂，超出 FR-190，放弃。
- **只允许运维手动上传 Worker 二进制**：可离线，但体验差且容易上传错版本；可作为后续兜底增强，不作为默认路径。
- **脚本匿名下载 Worker 二进制**：简单，但会扩大 CP 暴露面；本 FR 采用短期 token 限定下载上下文。
