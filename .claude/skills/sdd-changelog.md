---
name: sdd-changelog
description: 从 git log 生成 CHANGELOG 版本条目
---

# SDD Changelog 生成

## 触发

用户说 `/sdd-changelog` 或「生成 changelog」

## 执行步骤

1. 获取上次 tag 以来的 git log：
   ```bash
   git log $(git describe --tags --abbrev=0 2>/dev/null || echo "")..HEAD --oneline
   ```
2. 按 Conventional Commits type 分组：
   - `feat` → `### Added`
   - `fix` → `### Fixed`
   - `refactor` → `### Changed`
   - `perf` → `### Performance`
   - `docs` → `### Documentation`
3. 读取 `CHANGELOG.md`
4. 将当前 `[Unreleased]` 段内容移到新版本段：
   ```markdown
   ## [X.Y.Z] - YYYY-MM-DD

   ### Added
   - ...
   ```
5. 重置 `[Unreleased]` 段为空
6. 写入 `CHANGELOG.md`

## 约束

- 版本号遵循 SemVer（用户指定或自动推断）
- 条目使用中文描述（和 commit message 一致）
- 每个条目附带关联的 FR 编号（如可推断）
