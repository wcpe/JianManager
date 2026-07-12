# ADR-068: 客户端分发 zstd patch-from 增量发布

- **日期**: 2026-07-06
- **状态**: accepted
- **修订**: [ADR-054](054-updater-arch-simplification.md) 中对 `delta patch / 字节级 diff` 的否决结论
- **关联**: FR-098、FR-087、FR-257、ADR-021、ADR-054

## 上下文

ADR-054 为降低客户端更新器复杂度，否决了 CAS、`.jmpack` 容器和通用字节级 diff。FR-098 重新提出的是更窄的增量发布：不恢复 CAS，不恢复 `.jmpack`，只在现有逐文件 manifest 和 `/client-artifacts/:sha256` 分发链路上，为同路径同平台的上一版文件附加可选 zstd patch-from 制品。

## 决策

1. 发布侧在发布新版本时，仅相对当前频道 latest 的同路径、同平台文件生成 patch。完整 artifact 仍然保留，patch 只是 manifest 文件项上的可选加速路径。
2. patch codec 固定为 `zstd-patch`，patch artifact 继续按 `client-file` 内容寻址入库，并纳入资产删除保护与分发授权校验。
3. Control Plane 优先调用外部 `zstd --patch-from=<old> -o <patch> <new>` 直接基于文件路径生成 patch，避免大文件在 Go 堆内整块展开。未安装 `zstd` 时，只对小文件使用进程内 zstd 字典兜底；大文件缺少外部命令则跳过 patch，回退完整 artifact。
4. updater-core 只有在本地旧文件 sha256 等于 `patch.oldSha256` 时才下载 patch；patch 应用失败、hash 不符、旧文件不匹配时一律回退完整 artifact。
5. updater-core 用本地旧文件作为 zstd 字典解码 patch。受 zstd-jni raw dict API 限制，客户端只在旧文件低于固定堆内阈值时尝试 patch；超过阈值直接回退完整 artifact，避免大旧文件整块进入 Java 堆。输出仍写临时文件，sha256 校验通过后原子替换。

## 后果

- 继续保留 ADR-054 的简化信任模型：HTTPS + 拉取密钥 + sha256 完整性校验，不引入签名密钥、CAS 或 `.jmpack` 容器。
- 部署方若希望大资源包也生成 patch，需要在 Control Plane 运行环境安装 `zstd` 命令。
- patch 是优化路径，不是正确性前提；任何 patch 不可用都回退完整 artifact。
