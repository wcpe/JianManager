---
name: sdd-review
description: 审查代码实现是否和 SDD/API Spec 一致
---

# SDD 一致性审查

## 触发

用户说 `/sdd-review` 或「检查一下代码和 spec 是否一致」

## 执行步骤

1. 获取当前 diff（已修改的文件）
2. 识别修改涉及的模块和 feature
3. 读取对应的 `docs/specs/<feature>/api.md`
4. 读取 `docs/ARCHITECTURE.md` 相关章节
5. 逐项检查：

### API 一致性
- [ ] 新增/修改的 endpoint 路径和 API.md 一致
- [ ] 请求体字段和 API.md 一致
- [ ] 响应体字段和 API.md 一致
- [ ] 错误码和 API.md 一致
- [ ] 权限检查和 API.md 一致

### 架构一致性
- [ ] 新模块符合分层架构（router → service → model）
- [ ] 未引入 ARCHITECTURE.md 未记录的外部依赖
- [ ] 数据库 migration 和表结构定义一致

### 代码规范
- [ ] 遵循 `docs/conventions.md` 命名规范
- [ ] 错误处理符合规范（`fmt.Errorf` 包装）
- [ ] 日志使用 `slog` 结构化日志

6. 输出审查报告：

```
## SDD 审查报告

### ✅ 一致
- POST /api/v1/instances 实现和 spec 一致
- InstanceStatus 枚举和状态机一致

### ⚠️ 不一致
- API.md 定义了 `mcPort` 字段，但 model 中是 `mc_port`
  建议: 更新 API.md 为 `mcPort`（camelCase 约定）

### ❌ 缺失
- POST /api/v1/instances/:id/stop 缺少权限检查
  建议: 添加 `instance.operate` 权限校验
```
