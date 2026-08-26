# ADR-085：ServerProbe 本地上传来源

- 日期：2026-08-26
- 状态：accepted
- 关联：FR-411、ADR-011、ADR-083
- 修订：ADR-083 决策 2 的「当前仅 GitHub Releases 来源」实现范围

## 上下文

ADR-083 已将 ServerProbe 纳入 CAS 之上的制品包、来源和版本层，但首期只接入 GitHub Releases。运营者还需要将本地持有的探针 JAR 纳入同一版本选择与下发链路，并能清楚辨认线上拉取版本和本地上传版本。若另建平行上传库，会绕开既有 CAS 去重、引用保护和 CP→Worker 分发语义。

## 决策

1. `serverprobe` 制品包新增内置 `local-upload` 来源，展示名为“本地上传”；既有 `github-release` 来源展示为“GitHub Releases（线上拉取）”。两类来源都使用既有 `ArtifactSource`、`ArtifactVersion` 和 `assets` 模型，不新增表。
2. 仅平台管理员可通过 `POST /api/v1/artifact-packages/serverprobe/versions/upload` 以 `multipart/form-data` 上传 `version` 和一个不超过 64 MiB 的 `.jar`。CP 流式写入 `server-probe` 类型 CAS，自行计算校验和，不采信客户端摘要。
3. 成功上传立即创建已缓存的版本记录：`ReleaseRef=local-upload`、`AssetID`、`CachedAt` 和服务端计算的 SHA-256 必须齐全；`SourceURL` 使用仅作内部溯源的 `local://upload/<asset-id>`。同一 `local-upload` 来源内版本号唯一；不同来源允许同名版本。
4. 相同字节的线上和本地版本复用同一 CAS asset，但来源归属只由 `ArtifactVersion.SourceID` 判定，界面不得从共享 asset 的来源 URL 推断。上传不自动同步、不自动变更全局默认，也不修改已有实例。
5. 已缓存的本地上传版本复用 ADR-083 的三级选择和 CP-local 短期下载链路。Worker、gRPC 协议、短期令牌 scope 与原子替换语义不变；本地来源不提供“同步”操作。

## 后果

- 管理端需要提供上传表单，并在来源和版本处显示友好来源标签，避免同名版本歧义。
- 上传接口必须限制体积、校验扩展名并拒绝同一本地来源的重复版本；上传、CAS 入库与建版本记录在版本服务的互斥范围内完成，避免并发创建重复版本。
- `0.1.0` 可作为复制现有线上 JAR 的本地测试版本，验证来源、选择与下发链路；它不能证明不同代码版本的行为差异。
- 不引入其他外部来源、来源凭证、CP 本地路径导入、通用资产上传、代码签名/字节码验证或自动升级策略。

## 替代方案

- **继续只支持 GitHub Releases**：无法纳入本地构建或历史 JAR，否决。
- **另建 ServerProbe 上传目录**：重复 CAS 的存储、去重与引用保护，并分叉 Worker 下发路径，否决。
- **通过通用 assets 上传接口导入**：缺少 ServerProbe 版本、来源和选择语义，且会扩大不必要的通用资产写入面，否决。
- **上传后自动设为全局默认或推送旧实例**：会产生非显式版本变化，违反 ADR-083 的管理员显式选择与实例升级边界，否决。

## 关系

- **ADR-011**：继续提供 CAS 物理存储、流式入库、去重和引用保护。
- **ADR-083**：保留制品版本层、三级继承、CP-local Worker 拉取及不自动变更默认的原则；本 ADR 仅扩展 ServerProbe 的第二个内置来源。
