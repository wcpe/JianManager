# 功能规格：jm 命令行（gh 式）

> 状态：修订中（取代 FR-385 原「jm-agent 8 命令」定位）　·　关联 PRD：FR-385 / FR-560~439　·　ADR：076（沿用）、091
> 依赖：FR-560（user_token）、FR-562（device flow）

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

- `jm` 内部只允许依赖 `internal/platform/*`（keyring、httpclient、dataroot）与 `internal/version`
- `internal/version` 是唯一允许的非 `platform` 例外：单个文件、零外部依赖、只读常量，
  引入它是为了让 `--version` 与发行烟测有单一真值（否则安装包无法自证版本）
- **禁止**依赖 `internal/controlplane/service|mcp|model`、`proto/workerpb`、gRPC、GORM
- 目标：与 `jmctl` 同量级（当前 jmctl 依赖 3 个内部包）

### 3.7 分发与安装（FR-567）

`jm` 是 Go 单二进制，分发走两条独立通道；两者内容同源，只是触达方式不同。

**通道 A：GitHub Releases**

`make dist-jm` 交叉编译五件套（windows/linux/darwin × amd64/arm64，`CGO_ENABLED=0`），
由 `release.yml` 的 build 任务产出并随 Release 发布，附 `checksums.txt`。
`--version` 由 `-ldflags` 注入 `internal/version.Version`，供 smoke 校验。

**通道 B：npm 双包分发**

| 包 | 内容 | 作用 |
|---|---|---|
| `@jianmanager/jm` | 启动器 `bin/jm.js`（约 2.5 kB） | 定位平台二进制并 `spawn`，透传 stdio 与退出码 |
| `@jianmanager/jm-<platform>` ×5 | Go 二进制（约 7 MB） | `os`/`cpu` 限定，只装匹配当前平台的那个 |

平台标识与 npm 的 `process.platform-process.arch` 对齐：
`linux-x64`、`linux-arm64`、`darwin-x64`、`darwin-arm64`、`win32-x64`。

- 主包经 `optionalDependencies` 引用五个平台包；npm 按 `os`/`cpu` 自动跳过不匹配项
- **先发布平台分包、再发布主包**：反序会让安装者在平台包就位前解析失败
- 不用 `postinstall` 下载：企业内网/代理环境同样可装，且卸载升级由 npm 统一管理
- 平台清单在 `packages/jm-npm/package.json` 与发布脚本中各有一份，
  脚本启动时强制校验两者一致（漏改一侧的后果是「某平台装上后没有二进制」，只有该平台用户会遇到）

**包管理器选择**

文档只推荐 npm，原因是 npm 与 pnpm 的全局 `bin` 目录不同（`npm config get prefix`
vs `pnpm bin -g`），混用会让两份 `jm` 并存，PATH 里靠前者生效，版本问题极难排查。

功能上两者都支持：启动器优先用 `require.resolve('<pkg>/package.json')` 定位平台二进制，
Node 的模块解析天然处理 npm 平铺与 pnpm 的 `.pnpm` + 符号链接两种布局，
两条路径均已实测。硬编码路径探测仅作兜底——早先只写探测时正是因为路径多推导了一层，
导致「装好了却找不到二进制」。

### 3.8 `jm skill install` 的宿主目录约定

技能是宿主侧的静态文件，安装位置由宿主自己决定。`jm` 支持两个作用域：

| 作用域 | 含义 | 典型用途 |
|---|---|---|
| `user`（默认） | 装到本机用户目录，对该机器的所有项目生效 | 个人日常使用 |
| `project` | 装到当前仓库内，随仓库分发给协作者 | 项目自带技能库 |

**用户级根目录**（`~/.<host>/skills`）：

| 宿主 | 目录 |
|---|---|
| claude | `~/.claude/skills` |
| codex | `~/.codex/skills` |
| cursor | `~/.cursor/skills` |
| kiro | `~/.kiro/skills` |
| copilot | `~/.copilot/skills` |
| agents | `~/.agents/skills` |
| narrafork | `~/.narrafork/skills` |
| vantaloom | `<installDir>/data/skills`（见下方平台默认位） |

**项目级根目录**（相对项目根）：

| 宿主 | 目录 |
|---|---|
| claude / codex / cursor / kiro | `<ws>/.<host>/skills` |
| copilot | `<ws>/.github/skills` |
| agents | `<ws>/.agents/skills` |
| narrafork | `<ws>/.narrafork/skills` |
| vantaloom | `<ws>/.vantaloom/skills` |

`copilot` 与其他宿主不同，用 `.github/skills` 而不是 `.<host>`，这是 GitHub 侧的既有约定。

`narrafork` 与 `vantaloom` 是**自带技能库的应用**，各有原生目录，与通用 harness 不同：

- **NarraFork** 运行时按优先级链扫描：`~/.narrafork/skills` < `~/.narrafork/skill`
  < `~/.claude/skills` < `~/.agents/skills` < `<ws>/.narrafork/skills` < ...
  写入一律落在原生目录，避免被兼容目录的低优先级坑到。
- **Vantaloom** 运行时**只扫** `<ws>/.vantaloom/skills`（全局库为 `<installDir>/data/skills`）。
  它的 `.claude` / `.codex` / `.agents` 是**导入源**而非数据源，写进去不会被运行时装上——
  因此本项目级目标必须是 `.vantaloom/skills`，不是 `.claude/skills`。

Vantaloom 的库根有两处与其它宿主不同，都要对齐上游 `DefaultPrefix()` 与 `dataDir()`：

1. **多一层 `data`**：`server.go` 先取数据根（`<installDir>/data`），再拼 `skills`。
   因此真实位置是 `<installDir>/data/skills`——少写这层会落到 `<installDir>/skills`，
   那是升级前的旧位置，运行时不再读取。
2. **不在家目录**：安装前缀 `installDir` 由 `VANTALOOM_HOME` 决定，未设时按平台取默认：

   | 平台 | 默认 installDir | 技能库 |
   |---|---|---|
   | Windows | `D:\Vantaloom`（无 D 盘则 `C:\Vantaloom`） | `<installDir>/data/skills` |
   | macOS | `~/Applications/Vantaloom` | `<installDir>/data/skills` |
   | Linux 等 | `~/.local/vantaloom`（无家目录则 `/opt/vantaloom`） | `<installDir>/data/skills` |

   三处分支必须与上游保持一致，否则同一台机器上 `jm` 与 Vantaloom 会指向不同目录。

每个宿主统一装到 `<skills>/jianmanager/` 子目录：宿主按子目录发现技能，
SKILL.md 必须位于该子目录根部；平铺在 `skills/` 下会与其它技能混淆。

## 4. 不做

- ❌ 把 408 条路由逐个映射成子命令（正是 `gh` 避免的做法）
- ❌ OAuth 授权码 + PKCE（无回调场景）
- ❌ 读取 CP 的 action catalog 生成命令面（会让 CLI 链上 service 包）
- ❌ 本地保存明文 token

## 5. 任务拆分

- [x] 目录与命令面统一为 `jm`（`apps/jm`，`make build-jm` 产出 `jm`；`jmagent` 留作兼容别名）
- [x] `jm config` + `hosts.yml` + `--url`/`JM_URL` 优先级链
- [x] `jm api`（含 `--data @file|-`、`--query`、错误映射）
- [x] 现有 8 条糖迁移（输出格式不变）
- [x] `internal/platform/keyring` 接口 + Windows/macOS/Linux 三实现
- [x] `jm auth login/status/logout`（依赖 FR-562 device flow）
- [x] 单测：httpmock 断言 `jm api` 请求形态；keyring 用 fake
- [x] 真机验收：Windows 上 login → 调用 → logout → token 失效

## 6. 验收要点

- `jm api GET /api/v1/instances` 在登录后能列出实例
- `jm --url <临时> api ...` 覆盖默认 host
- `jm auth logout` 后同一个 token 调用返回 401
- `go list -deps ./apps/jm` 中本项目包数 ≤ 5
