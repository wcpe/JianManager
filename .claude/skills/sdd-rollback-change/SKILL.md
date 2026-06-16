---
name: sdd-rollback-change
description: 回滚有问题的变更（安全撤销最近的提交或部署）
---

# 回滚变更

## 触发

用户说 `/sdd-rollback-change` 或「回滚」或「撤回上次变更」

## 策略选择

根据情况选择回滚策略：

### 策略 A: Git Revert（推荐，最安全）

适用于：已推送到远程的提交

1. 确认要回滚的 commit：
   ```bash
   git log --oneline -10
   ```
2. 创建 revert commit：
   ```bash
   git revert <commit-hash>
   ```
3. 如果是多个 commit，逐个 revert（从新到旧）
4. 推送 revert commit

### 策略 B: Git Reset（仅限本地未推送的提交）

适用于：本地开发中发现的问题

1. 确认要回滚到的 commit
2. 保留工作区变更（软回滚）：
   ```bash
   git reset --soft HEAD~1
   ```
3. 或丢弃工作区变更（硬回滚）：
   ```bash
   git reset --hard HEAD~1
   ```

### 策略 C: 功能开关（适用于已部署的功能）

适用于：不想回滚代码，只想关闭功能

1. 在代码中添加 feature flag
2. 通过配置关闭该功能
3. 后续修复后再开启

## 回滚后

1. 确认系统恢复正常
2. 分析问题根因
3. 创建 FR 记录需要的修复
4. 更新 `CHANGELOG.md` 记录回滚事件
5. 如果回滚影响了文档，同步更新

## 约束

- 回滚前先评估影响范围（是否影响数据库 migration？是否影响其他 feature？）
- 数据库 migration 的回滚需要特别谨慎（可能丢失数据）
- 回滚不是修复，回滚后仍需修复问题
