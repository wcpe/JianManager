# Git 提交规范

> 适用于本仓库所有 `git commit` 操作。

## 1. 提交信息语言（强制）

- **标题（Description）与正文（Body）必须使用简体中文。** 禁止英文、日文等非中文。
- Conventional Commits 的 type 与 scope 仍用英文小写（`feat`/`fix`/`refactor`/`docs`/`chore`/`test`/`build`/`ci`/`perf`/`style`）。
- **禁止在提交信息中添加任何 AI 签名或尾注**，例如 `Generated with ...`、`Co-Authored-By: ...`。不要附加作者/工具/来源署名。

### 1.1 标题格式

```
<type>(<scope>): <中文描述>
```

- `<scope>`：英文小写模块/能力域，可选。常用：`control-plane`、`worker`、`bot-worker`、`web`、`proto`、`config`、`api`、`build`、`ci`、`docs`、`sdd`、`deps`。
- `<中文描述>`：简洁陈述本次做了什么，必须中文，结尾不加句号。

### 1.2 正文格式

- 用空行与标题分隔，中文撰写，可用 `-` 列要点。
- 说明"为什么改"与"改动要点"，不逐行复述 diff。

## 2. Type 枚举

| type | 含义 | 常用场景 |
|---|---|---|
| `feat` | 新功能 | 新增 FR、新模块、新 endpoint |
| `fix` | Bug 修复 | 修复缺陷、回归问题 |
| `refactor` | 重构 | 不改变外部行为的代码改进 |
| `docs` | 文档变更 | PRD/ARCHITECTURE/API/ADR 更新 |
| `chore` | 杂项 | 依赖升级、配置调整、构建脚本 |
| `test` | 测试 | 新增/修改测试 |
| `build` | 构建 | 构建脚本、Dockerfile、CI 流水线 |
| `ci` | CI/CD | CI 配置变更 |
| `perf` | 性能优化 | 提升性能但不改变行为 |
| `style` | 代码风格 | 格式化、命名调整（不影响逻辑） |

## 3. Scope 枚举

| scope | 含义 |
|---|---|
| `control-plane` | Control Plane 后端变更 |
| `worker` | Worker Node 变更 |
| `bot-worker` | Bot Worker (Node.js) 变更 |
| `web` | 前端 (React) 变更 |
| `proto` | Protobuf 定义变更 |
| `config` | 配置文件变更 |
| `api` | API 定义变更 |
| `build` | 构建脚本/Dockerfile 变更 |
| `ci` | CI/CD 流水线变更 |
| `docs` | 文档变更 |
| `sdd` | SDD 体系（规则/技能）变更 |
| `deps` | 依赖版本变更 |

## 4. 示例

### 标题 + 正文

```
feat(worker): 实现守护进程二进制帧协议

- 基于 ADR-003 设计，8 字节帧头 + zlib 压缩
- 支持 STDIN/STDOUT/STDERR/CONTROL 四个通道
- 实现帧编解码和心跳机制
```

```
fix(control-plane): 修复 token 刷新并发竞态

- 两个请求同时用同一个 refreshToken 刷新时会产生重复 token
- 改为数据库行锁保证刷新操作串行化
```

```
docs: 更新 API.md 新增 bot 接口定义

- 补充 POST /bots、DELETE /bots/:id、POST /bots/:id/behavior
- 同步更新 ARCHITECTURE.md 中 Bot Worker 通信协议描述
```

### 仅标题（无需正文时）

```
refactor(worker): 提取 ProcessManager 接口便于测试
```

```
chore(deps): 升级 golang.org/x/net 到 v0.24.0
```

## 5. 禁止事项

- ❌ 英文描述（type 和 scope 除外）
- ❌ `update code`、`fix bug`、`wip`、`misc` 等无意义描述
- ❌ 标题超过 72 个字符
- ❌ 标题结尾加句号
- ❌ `feat`/`fix` 不带 scope（必须标注影响的模块）
- ❌ 正文中逐行复述 diff（应说明"为什么改"和"改动要点"）
- ❌ 任何 AI 签名或尾注（`Generated with ...`、`Co-Authored-By: ...`）
- ❌ 混合不相关变更在一个 commit 中（一个 commit 做一件事）
- ❌ 提交信息中出现阶段性词语（`Phase 0`、`Phase 1`、`P0`、`P1`、`P2`、`MVP`、`Sprint`、`迭代`）

## 6. 最小提交粒度

每个 commit 必须满足：

- **独立可编译**：`go build ./...` 或 `npm run build` 必须通过
- **单一职责**：一个 commit 只做一件事（一个功能点 / 一个修复 / 一块逻辑改动）
- **不混合类型**：feat 和 fix 不在同一个 commit，refactor 和 feat 不在同一个 commit

### 合格示例

```
feat(worker): 实现实例状态机 STOPPED→STARTING→RUNNING 转换

fix(control-plane): 修复并发创建实例时配额检查竞态

refactor(worker): 提取 IProcessCommand 接口
```

### 不合格示例

```
❌ feat(worker): Phase 1 实现实例管理和终端和文件管理
   → 拆成三个 commit，每个独立可编译

❌ feat(worker+web): 实现实例管理前端和后端
   → 拆成两个 commit，后端先提交，前端后提交

❌ feat(worker): WIP 实现实例状态机，还差测试
   → 完成后再提交，或者拆出已可编译的部分单独提交
```
