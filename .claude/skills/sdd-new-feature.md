---
name: sdd-new-feature
description: 为新 Feature 生成文档骨架（api.md + impl.md），更新 PRD 状态
---

# SDD 新 Feature 文档生成

## 触发

用户说 `/sdd-new-feature FR-XXX <feature-name>` 或「开始新功能 XXX」

## 执行步骤

1. 读取 `docs/PRD.md`，找到指定 FR 的内容（描述、验收标准、优先级）
2. 创建目录 `docs/specs/<feature-name>/`
3. 生成 `docs/specs/<feature-name>/api.md` 骨架：

```markdown
# API — <Feature Name>

> 关联 FR: FR-XXX

## TODO
<!-- 由 /sdd-gen-api 填充 -->

### ENDPOINT_1
- **描述**: ...
- **方法**: GET/POST/PUT/DELETE
- **路径**: /api/v1/...
- **权限**: ...
- **请求**: ```json ```
- **响应**: ```json ```
- **错误码**: ...
```

4. 生成 `docs/specs/<feature-name>/impl.md` 骨架：

```markdown
# 实施计划 — <Feature Name>

> 关联 FR: FR-XXX | 优先级: PX | 状态: 📋 todo

## 任务拆解

### Phase 1: 后端
- [ ] task 1
- [ ] task 2

### Phase 2: 前端
- [ ] task 3

### Phase 3: 联调测试
- [ ] task 4

## 依赖
- 依赖的其他 feature / 外部库

## 风险
- 已知风险和应对方案
```

5. 更新 `docs/PRD.md` 中该 FR 状态为 `🔨 in-progress`
6. 输出摘要告知用户
