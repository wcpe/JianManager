---
name: sdd-release-version
description: 正式版本发布流程（版本号 → 构建 → CHANGELOG → tag → 打包）
---

# 正式版本发布

## 触发

用户说 `/sdd-release-version vX.Y.Z` 或「发布 vX.Y.Z」或「准备发版」

## 流程

### Phase 1: 发版前检查

1. **Gate 4 检查**：按 `.claude/rules/gate-merge.md` checklist 逐项核对
   - 所有 in-progress 的 FR 必须是 done 或 deferred
   - 文档必须同步
   - 测试必须全部通过
2. 确认无未解决的 blocking issue

### Phase 2: 版本号

3. 确定版本号（用户指定或 SemVer 推断）：
   - 破坏性变更 → major
   - 新功能 → minor
   - Bug 修复 → patch
4. 更新代码中的版本号常量（如有）

### Phase 3: CHANGELOG

5. 运行 `/sdd-changelog` 生成版本条目
6. 审核 CHANGELOG 内容
7. 确认所有 FR 编号引用正确

### Phase 4: 构建

8. 构建前端 + Bot Worker + Go 二进制
9. 交叉编译（linux/amd64, windows/amd64）
10. 运行全量测试

### Phase 5: 发布

11. 提交版本变更：
    ```
    release: vX.Y.Z
    ```
12. 创建 tag：
    ```bash
    git tag -a vX.Y.Z -m "Release vX.Y.Z"
    ```
13. 推送 tag

### Phase 6: 打包

14. 打包发布产物：
    ```
    jianmanager-vX.Y.Z-linux-amd64.tar.gz
    jianmanager-vX.Y.Z-windows-amd64.zip
    ```
15. 如有 Docker 镜像，构建并推送

## 约束

- 发版前必须通过 Gate 4 全部检查
- 版本号必须遵循 SemVer
- CHANGELOG 必须覆盖所有自上次发版以来的变更
- 不得在发版 commit 中混入功能代码
