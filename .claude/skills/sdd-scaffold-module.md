---
name: sdd-scaffold-module
description: 从 API Spec 生成 Go 后端模块骨架代码
---

# SDD Go 模块脚手架

## 触发

用户说 `/sdd-scaffold-module <module-name>` 或「搭一下 XXX 模块骨架」

## 执行步骤

1. 读取 `docs/specs/<module-name>/api.md` 获取 endpoint 列表
2. 读取 `docs/ARCHITECTURE.md` 获取数据库模型
3. 读取 `docs/conventions.md` 获取编码规范
4. 生成以下文件：

### Control Plane 侧

**`internal/controlplane/model/<module>.go`** — GORM Model
- 从 ARCHITECTURE.md 表结构生成 struct
- 字段 tag：`gorm:""` + `json:""`
- 关联关系

**`internal/controlplane/router/<module>.go`** — Gin Handler
- 每个 endpoint 一个 handler 方法
- 参数绑定 + 调用 service + 响应
- 注册路由函数 `Register<Routes>(rg *gin.RouterGroup)`

**`internal/controlplane/service/<module>.go`** — 业务逻辑
- 每个 handler 对应一个 service 方法
- 参数校验、业务逻辑、数据库操作
- 错误返回用预定义 error 常量

### Worker Node 侧（如需要）

**`internal/worker/grpc/handler_<module>.go`** — gRPC Handler
- 实现 proto 生成的 interface

5. 在 `internal/controlplane/router/router.go` 中注册新模块路由
6. 输出已生成文件清单

## 约束

- 遵循 `docs/conventions.md` 中的 Go 编码规范
- 不写业务实现，只生成骨架（方法体 `// TODO`）
- model 字段和 ARCHITECTURE.md 严格一致
