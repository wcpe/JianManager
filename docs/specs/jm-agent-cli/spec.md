# 功能规格：jm 命令行（gh 式）

> 状态：修订中（取代 FR-385 原「jm-agent 8 命令」定位）　·　关联 PRD：FR-385 / FR-513~439　·　ADR：076（沿用）、091
> 依赖：FR-513（user_token）、FR-515（device flow）

## 1. 背景与目标

把原 `jmagent`（8 条固定命令、Agent Token）重构为 **`jm`**，对齐 GitHub `gh` 的形态：

- 一条 `jm api` 覆盖 CP 全部 HTTP 路由（当前 408 条），不再逐个映射子命令
- 保留少量高频命令作为「糖」
- 引入 `jm auth`（device flow）与 `jm config`（多 host）
- 认证从 Agent Token 扩展为**用户 PAT（user_token）**

**与 `jmctl` 严格区分**：jmctl = 本机紧急直连 daemon，零网络面；`jm` = 经 CP HTTPS 的日常运维面。

## 2. 定位对照

| | `gh` | `jm` |
|---|---|---|
| 通用层 | `gh api <method> <path>` | `jm api <method> <path>` |
| 认证 | `gh auth login`（device flow / token） | `jm auth login`（device flow） |
| 配置 | `gh config set/get`，多 hostname | `jm config set-url/use/list`，多 host |
| 高频糖 | `gh pr/issue/repo/...`（34 组） | `jm instance/node/bot`（8~15 条） |
| 凭证存储 | 系统 keyring | **系统 keyring（三平台）** |
| 远端吊销 | `gh auth logout` 只删本地 | `jm auth logout` **本地删 + 远端吊销** |

## 3. 需求

### 3.1 `jm api`（核心）

```
jm api GET  /api/v1/instances
jm api POST /api/v1/instances/12/start
jm api GET  /api/v1/nodes --query page=1 --query pageSize=20
jm api POST /api/v1/users --data @body.json
echo '{"name":"x"}' | jm api POST /api/v1/instances --data -
```

- 自动补 base URL（来自 `jm config`）
- 自动带 `Authorization: Bearer <user_token>`
- 原始 JSON 输出到 stdout（不加工，便于 `jq`）
- `--data @file` / `--data -`（stdin）支持大 body，避免 shell 转义问题
- 非 2xx：错误体原样输出 + 非零退出码，并在 stderr 给出可读摘要

### 3.2 高频糖（保留现有 8 条）

`whoami`、`list nodes`、`list instances [--node]`、`instance status|metrics|start|stop|restart <id>`、
`node maintenance enter|leave <id>`。

糖的实现可以是 `jm api` 的内联封装，但**对外行为（输出格式）保持不变**，避免破坏既有脚本。

### 3.3 `jm auth`

```
jm auth login [--host <url>]     # device flow，终端显示 user_code + URL
jm auth status                   # 显示 host / 用户 / token 前缀 / scope / 过期
jm auth logout [--host <url>]    # 本地删除 + 远端吊销
```

`logout` 必须：
1. 调 `DELETE /api/v1/user/tokens/:id` 远端吊销
2. 删除 keyring 条目
3. 远端失败时不阻断本地清理，但明确提示「远端未吊销，请到 Web 设置页手动处理」

### 3.4 `jm config`（多 host）

```
jm config set-url https://cp.example.com     # 设为默认
jm config get-url
jm config list                                # 列出全部 host 与默认标记
jm config use staging                         # 切换默认
jm config remove staging
```

- 永久配置存 `~/.config/jm/hosts.yml`
- **临时覆盖**：全局 `--url <url>` flag，优先级 `--url` > 当前默认 host
- 环境变量 `JM_URL` / `JM_TOKEN` 仍支持（CI 场景），优先级低于 flag 高于配置文件
- 每个 host 独立 token 与用户名

### 3.5 凭证存储（三平台 keyring）

- 接口：`internal/platform/keyring`（`Set/Get/Delete`，key 为 `<host>`）
- Windows：Credential Manager（`wincred`）
- macOS：Keychain（`security` 命令或 `go-keyring`）
- Linux：Secret Service（`libsecret` / D-Bus）
- keyring 不可用（无 D-Bus、容器内）→ **报错并提示**，不静默写明文
- 允许 `JM_TOKEN` 环境变量作为 CI 场景的旁路

### 3.6 依赖约束（硬性）

- `jm` 内部只允许依赖 `internal/platform/*`（keyring、httpclient、dataroot）
- **禁止**依赖 `internal/controlplane/service|mcp|model`、`proto/workerpb`、gRPC、GORM
- 目标：与 `jmctl` 同量级（当前 jmctl 依赖 3 个内部包）

## 4. 不做

- ❌ 把 408 条路由逐个映射成子命令（正是 `gh` 避免的做法）
- ❌ OAuth 授权码 + PKCE（无回调场景）
- ❌ 读取 CP 的 action catalog 生成命令面（会让 CLI 链上 service 包）
- ❌ 本地保存明文 token

## 5. 任务拆分

- [ ] 包改名 `apps/jmagent` → 命令面改为 `jm`（`make build-jm` 产出 `jm`）
- [ ] `jm config` + `hosts.yml` + `--url`/`JM_URL` 优先级链
- [ ] `jm api`（含 `--data @file|-`、`--query`、错误映射）
- [ ] 现有 8 条糖迁移（输出格式不变）
- [ ] `internal/platform/keyring` 接口 + Windows/macOS/Linux 三实现
- [ ] `jm auth login/status/logout`（依赖 FR-515 device flow）
- [ ] 单测：httpmock 断言 `jm api` 请求形态；keyring 用 fake
- [ ] 真机验收：Windows 上 login → 调用 → logout → token 失效

## 6. 验收要点

- `jm api GET /api/v1/instances` 在登录后能列出实例
- `jm --url <临时> api ...` 覆盖默认 host
- `jm auth logout` 后同一个 token 调用返回 401
- `go list -deps ./apps/jm` 中本项目包数 ≤ 5
