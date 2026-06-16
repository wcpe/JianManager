# CHANGELOG

> 本文档累积更新，每次发版新增一个版本段。

---

## [Unreleased]

### Added
- SDD 开发体系（文档 + 规则 + 技能）
- PRD, ARCHITECTURE, API, ADR 基础文档

#### 核心功能 (P0)
- 用户认证：JWT 双 Token（15min access + 7d refresh），bcrypt 密码加密
- 用户与权限：平台管理员/组管理员/组成员三级角色，RBAC 权限中间件
- 用户组与配额：组 CRUD、成员管理、实例配额检查
- 节点注册：gRPC 注册/心跳/离线检测（90s 超时）
- 实例生命周期：状态机 + 指数退避自动重启
- 守护进程：二进制帧协议 + PID 文件恢复 + 环形缓冲区
- 终端：gorilla/websocket + xterm.js + 30s 一次性 token
- 文件管理：目录浏览 + 在线编辑 + 上传下载 + 路径安全检查

#### 重要功能 (P1)
- Bot 平台：行为引擎框架（idle/follow/patrol/guard）
- 监控指标：gopsutil 采集 + Recharts 仪表盘
- 告警规则：阈值触发 + Webhook 通知
- 定时任务：Cron 调度器
- 备份恢复：手动创建/列表/删除

#### 增强功能 (P2)
- 服务端模板：CRUD + 卡片式展示
- 审计日志：中间件自动记录 + 查询
- i18n：i18next 中英文

#### 前端
- 11 个管理页面 + 实例详情页（6 Tab）
- 文件浏览器 + 终端组件 + 创建实例对话框

#### 基础设施
- go:embed 前端嵌入，单二进制部署
- Docker 部署
- gRPC 协议桩代码
- API 限流中间件
- 15 个单元测试
