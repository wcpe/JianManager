---
name: sdd-bump-dependencies
description: 检查并升级项目依赖（Go modules + npm packages）
---

# 依赖升级

## 触发

用户说 `/sdd-bump-dependencies` 或「升级依赖」

## 执行步骤

### Go 依赖

1. 列出可用更新：
   ```bash
   go list -m -u all | grep '\[' 
   ```
2. 逐个评估更新风险（major/minor/patch）
3. minor/patch 更新直接升级：
   ```bash
   go get -u <package>
   go mod tidy
   ```
4. major 更新需要评估 API 兼容性，列出影响范围供用户确认
5. 运行 `go test ./...` 确认无破坏

### npm 依赖 (web)

1. 检查可用更新：
   ```bash
   cd web && npm outdated
   ```
2. minor/patch 更新：
   ```bash
   cd web && npm update
   ```
3. major 更新列出清单供用户确认
4. 运行 `npm run build` 确认无破坏

### npm 依赖 (bot-worker)

1. 同上流程：
   ```bash
   cd bot-worker && npm outdated
   ```

### 收尾

1. 更新 `CHANGELOG.md`：
   ```markdown
   ### Changed
   - chore(deps): 升级 golang.org/x/net v0.22.0 → v0.24.0
   - chore(deps): 升级 @tanstack/react-query 5.20.0 → 5.25.0
   ```
2. 运行全量测试确认无回归
3. 提交：`chore(deps): 升级依赖到最新版本`

## 约束

- 不得一次性升级所有 major 版本（逐个评估）
- 升级后必须通过全部测试
- 有破坏性变更的升级需要更新相关文档
