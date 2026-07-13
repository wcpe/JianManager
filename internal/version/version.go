package version

// Version 是「当前应报告的版本」的单一真值来源，由发版流程维护，命名遵循
// `.claude/rules/versioning.md`（见 ADR-065）：
//   - 正式发布态：裸 `X.Y.Z`，仅出现在打了 tag `vX.Y.Z` 的那个提交上。
//   - 开发态（两个 tag 之间）：`X.Y.Z-dev`，其中 `X.Y.Z` 是下一个计划发布的目标版本；
//     `-dev` 是 SemVer 预发布标识，语义「正朝 X.Y.Z 前进但尚未发布」，
//     优先级天然满足 `X.Y.(Z-1) < X.Y.Z-dev < X.Y.Z`，不构成漂移。
// 真值由发布/构建管线（FR-173、`make dist`）在 go build 时经 -ldflags 注入覆盖此默认值：
// go build -ldflags "-X github.com/wcpe/JianManager/internal/version.Version=X.Y.Z"
var Version = "0.16.0"
