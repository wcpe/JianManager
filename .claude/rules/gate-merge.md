# Gate 4: 合并/发版门禁

> 代码合并到 main 前必须满足以下条件。

## Checklist

### 一致性
- [ ] 实现和 `docs/specs/<feature>/api.md` 一致
- [ ] 实现和 `docs/ARCHITECTURE.md` 架构一致（未引入未记录的模式）
- [ ] 数据库 migration 和 ARCHITECTURE.md 中的表结构一致

### 文档同步
- [ ] `docs/ARCHITECTURE.md` 已更新（如有架构变更）
- [ ] `docs/API.md` 已更新（如有 API 变更）
- [ ] `docs/adr/` 已追加（如有架构决策变更）
- [ ] `docs/PRD.md` 中对应 FR 状态已更新

### 代码质量
- [ ] 无编译错误
- [ ] 无 lint 错误
- [ ] 核心逻辑有测试覆盖

### 发版时额外检查
- [ ] `CHANGELOG.md` 已更新
- [ ] 版本号已确定

## 检查方式

按此 checklist 逐项核对。
