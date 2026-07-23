# 功能规格：jm-agent 管理 CLI

> 状态：草拟（并行前置）　·　关联 PRD：FR-385　·　依赖：FR-384　·　ADR：076（沿用）

## 1. 背景与目标

为脚本 / CI / 本机 shell 提供经 CP HTTPS + Agent Token 的管理入口。  
**与 `jmctl` 严格区分**：jmctl = 本机紧急直连 daemon；jm-agent = 经 CP 管理面。

## 2. 需求（要什么）

### 范围内
- 二进制 `apps/jmagent/`（或 `apps/jm-agent/`，落地与 monorepo 约定一致）
- 配置：`--token` / `--cp-url`；env `JM_AGENT_TOKEN` / `JM_AGENT_CP`
- 命令：
  - `whoami`
  - `list nodes` / `list instances [--node <id>]`
  - `instance status|metrics|logs <id>`（logs 支持 `--tail N`）
  - `instance start|stop|restart <id>`
  - `node maintenance enter|leave <id>`
- 输出：默认 text；`--output json` 给脚本
- 403/硬拒绝：stderr 中文 + 非零退出码；不落盘 Token

### 不做
- 不链 gRPC/DB/Worker；不直连 daemon
- 不实现 Token 颁发（走面板/API）

## 3. 设计（怎么做）

- 薄 HTTP 客户端调 FR-384 暴露的 Agent API
- CLI 框架：优先标准库 `flag`/`cobra`（与 `apps/jmctl` 一致则复用风格）
- 依赖闭包保持轻量（目标 ~10MB 量级）

## 4. 任务拆分

- [ ] 脚手架 main + 配置解析
- [ ] whoami / list / instance / node 子命令
- [ ] JSON/text 输出与错误映射
- [ ] 单元测试（mock HTTP）
- [ ] Makefile/构建目标
- [ ] 文档同步

## 5. 验收标准

1. 同 Token 下 CLI 与 curl 行为一致（成功/403）
2. 写白名单外、scope 外、硬拒绝三类失败可断言
3. Token 仅 env/`--token`；不写日志文件
4. 不引入 gRPC/DB 依赖（`go list` 校验）

## 6. 风险 / 待定

- 包路径 `apps/jmagent` vs `apps/jm-agent` 以 monorepo 惯例为准
