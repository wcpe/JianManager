---
name: sdd-fix-bug
description: Bug 修复工作流（复现 → 定位 → 修复 → 验证 → 提交）
---

# Bug 修复工作流

## 触发

用户说 `/sdd-fix-bug` 或「修复这个 bug」或报告了一个问题

## 流程

### Phase 1: 复现

1. 理解 bug 描述（用户报告、错误日志、截图）
2. 确定复现步骤
3. 确定影响范围（哪个模块、哪些 FR 受影响）

### Phase 2: 定位

4. 分析根因：
   - 读取相关代码（router → service → model）
   - 检查相关 API Spec 是否和实现一致
   - 检查是否违反了 ARCHITECTURE.md 中的约束
5. 确认根因后，判断：
   - 是代码 bug → Phase 3
   - 是 spec 设计问题 → 更新 spec，再 Phase 3
   - 是 ADR 需要变更 → 创建新 ADR，再 Phase 3

### Phase 3: 修复

6. 编写修复代码
7. 编写回归测试（防止再次出现）
8. 运行相关测试确认通过

### Phase 4: 验证

9. 手动验证修复效果
10. 运行 `/sdd-review` 确认修复没有引入新的不一致
11. 检查修复是否需要文档同步（通常不需要，除非改了 API 行为）

### Phase 5: 提交

12. 提交：`fix(<scope>): 修复<bug描述>`
    - body 中说明根因和修复方案
    - 如有相关 issue 编号，附在 footer

## 约束

- Bug 修复不得引入新功能（范围纪律）
- 必须有回归测试
- 提交 message 中必须说明根因
