# 版本号规范

> 适用于本仓库所有版本号出现的地方：`internal/version/version.go`、tag、CHANGELOG、PRD 状态、构建产物、自更新/内嵌资产。见 ADR-065。

## 单一真值来源

`internal/version/version.go` 的 `var Version` 是「当前应报告的版本」的**唯一真值**。构建/发布管线（`make dist`、CI，见 ADR-036）在 `go build` 时经 `-ldflags -X …version.Version=` 注入覆盖，但**源码默认值必须始终等于当前应报告的版本**，不得停留在旧值造成漂移。

## 三种版本形态

| 形态 | 格式 | 何时 | 谁写 |
|---|---|---|---|
| **正式发布版** | 裸 `X.Y.Z`（无后缀） | 仅存在于打了 tag `vX.Y.Z` 的那个提交上，与 tag 一一对应 | `sdd-release-version` |
| **开发版（快照）** | `X.Y.Z-dev` | 两个正式 tag 之间的**所有开发态提交**（`dev` 分支常态） | `sdd-release-version` 在上个 tag 后立即置入 |
| **候选版**（可选） | `X.Y.Z-rc.N` | 临近发布做发布候选时，`N` 从 1 单调递增 | 发版流程 |

- `X.Y.Z-dev` 里的 `X.Y.Z` = **下一个计划发布的目标版本号**，按 SemVer 依据「自上个 tag 以来累积的提交类型」推断：有 `feat`→MINOR、仅 `fix`/`perf`/`refactor`→PATCH、有破坏性变更→MAJOR。
- `-dev` / `-rc.N` 是 SemVer **预发布标识**，优先级天然正确：`0.14.0 < 0.15.0-rc.1 < 0.15.0-dev`（注：`dev` > `rc` 按字典序，故临近发布用 `rc` 时应显式切换而非并存）——实践中开发期只用 `X.Y.Z-dev`，`rc` 仅在冻结候选时短暂使用。关键不变式：**`上个正式版 < 开发版 < 目标正式版`**。

## 状态流转（谁在什么时候改 version.go）

1. **上个版本 `vX.Y.Z` 打 tag 之后立刻**：`sdd-release-version` 把 `version.go` bump 到下一目标的 `-dev`（例：发完 `0.14.0` → 立即置 `0.15.0-dev`），**重新打开开发窗口**。
2. **开发期间目标版本号需上调**（如中途合入破坏性变更，MINOR→MAJOR）：直接改 `-dev` 前面的 `X.Y.Z`，无需其他动作。降级目标（把已宣布的下一版号往回改）不允许。
3. **正式发布时**：`sdd-release-version` 去掉 `-dev` 后缀 → 裸 `X.Y.Z`，打 tag `vX.Y.Z`，随后**再 bump 到下一个 `-dev`**。tag 提交本身是唯一携带裸 `X.Y.Z` 的提交。

## 与既有规则的对齐（不新增矛盾）

- **PRD**（`doc-evolution.md`）：FR 在开发版交付即标 `✅ 已交付@vX.Y.Z`，其中 `X.Y.Z` = 当前 `-dev` 的目标版本号（与「正式 tag 前可标下一开发版号」一致）。**禁止裸 `✅ 已交付`**。
- **CHANGELOG**（`doc-evolution.md`）：`[Unreleased]` 段即当前 `-dev` 的归档区，段首注明「本段为 `vX.Y.Z` 开发版归档区」，`X.Y.Z` 与 `version.go` 的 `-dev` 目标一致。
- **快照发布**（`sdd-publish-snapshot`）：发布物报告 `X.Y.Z-dev`；如需区分多次快照，由构建管线附加 **SemVer 构建元数据** `+g<shortsha>`（不影响优先级、不改变可比性），`version.go` 源值恒为 `X.Y.Z-dev`，**不手工递增计数**。
- **产物命名**（ADR-036）：不变——`<component>-<os>-<arch>[.exe]` 不含版本；版本经 ldflags 注入二进制内部，自更新/资产路径 `/worker-assets/<version>/…` 用注入值。
- **内嵌 Worker 清单**（FR-278）：`make embed-worker --version $(VERSION)` 与 `version.Version` **同源同值**，`-dev` 后缀两侧一致，资产解析的「精确字符串相等」匹配不受影响。
- **自更新版本比较**：`versionDiffers()` 只判等不做序比较，`-dev` 后缀安全；不得引入对 `version.Version` 的严格 `^\d+\.\d+\.\d+$` 正则校验（会误拒预发布号）。

## 检查

- 任何时刻 `git describe` / `version.Version` 报的版本，要么是「已 tag 的裸 `X.Y.Z`」，要么是「带 `-dev` 的下一目标版」，**不存在第三种**。
- Code Review / 发版门禁（`gate-merge.md`）核对：`version.go`、CHANGELOG `[Unreleased]` 目标、PRD `✅ 已交付@` 版本号三者对同一个 `X.Y.Z`。
- 发现 `version.go` 停留在已发布的裸版本号（如已 tag `v0.14.0` 后仍写 `0.14.0`）即为漂移，必须补 `-dev`。
