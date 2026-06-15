---
name: sdd-publish-snapshot
description: 发布开发快照版本（非正式发布，供测试使用）
---

# 发布快照版本

## 触发

用户说 `/sdd-publish-snapshot` 或「发个 snapshot」或「出个测试版」

## 流程

### Phase 1: 版本号

1. 确定当前最新 tag：
   ```bash
   git describe --tags --abbrev=0
   ```
2. 生成 snapshot 版本号：`vX.Y.Z-snapshot.N`（N 递增）
   - 如当前是 `v0.1.0`，snapshot 为 `v0.1.1-snapshot.1`

### Phase 2: 构建

3. 构建前端：
   ```bash
   cd web && npm run build
   ```
4. 构建 Bot Worker：
   ```bash
   cd bot-worker && npm run build
   ```
5. 交叉编译 Go：
   ```bash
   GOOS=linux GOARCH=amd64 go build -o dist/control-plane-linux ./cmd/control-plane
   GOOS=linux GOARCH=amd64 go build -o dist/worker-linux ./cmd/worker
   GOOS=windows GOARCH=amd64 go build -o dist/control-plane.exe ./cmd/control-plane
   GOOS=windows GOARCH=amd64 go build -o dist/worker.exe ./cmd/worker
   ```

### Phase 3: 打包

6. 打包为 tar.gz / zip：
   ```bash
   tar -czf jianmanager-$VERSION-linux-amd64.tar.gz -C dist control-plane-linux worker-linux bot-worker/
   ```

### Phase 4: 发布

7. 创建 git tag：
   ```bash
   git tag $VERSION
   ```
8. 更新 `CHANGELOG.md` 的 `[Unreleased]` 段

## 约束

- Snapshot 不得用于生产环境
- Snapshot tag 不参与正式版本的 changelog 生成
- 可以包含未完成的 feature（但不得包含已知的严重 bug）
