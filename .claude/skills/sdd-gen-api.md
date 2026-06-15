---
name: sdd-gen-api
description: 从 SDD/数据库模型生成完整的 API Spec
---

# SDD API Spec 生成

## 触发

用户说 `/sdd-gen-api <feature-name>` 或「生成 XXX 的 API 定义」

## 执行步骤

1. 读取 `docs/specs/<feature-name>/api.md` 骨架
2. 读取 `docs/ARCHITECTURE.md` 中的通信协议和数据库模型
3. 读取 `docs/PRD.md` 中对应 FR 的验收标准
4. 为每个 endpoint 生成完整定义：
   - HTTP 方法 + 路径
   - 请求体（JSON 结构，字段名 camelCase，和数据库 model 对应）
   - 响应体（JSON 结构）
   - 错误码（业务错误码 + HTTP 状态码）
   - 权限要求
   - 关联 FR 编号
5. 写入 `docs/specs/<feature-name>/api.md`
6. 同步更新 `docs/API.md` 对应章节（追加或更新）
7. 输出摘要

## 约束

- JSON 字段名用 camelCase（前端约定）
- 数据库字段名用 snake_case（Go/SQL 约定）
- 每个 endpoint 必须标注关联 FR
- 错误响应统一格式：`{ "error": "CODE", "message": "...", "details": {} }`
