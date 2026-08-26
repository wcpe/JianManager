# 功能规格：Linux 用户级 systemd 版本化直装与节点部署

> 状态：✅ 已交付@v0.21.0　·　关联 PRD：FR-410　·　关联 ADR：ADR-084（accepted）

## 1. 背景与目标

现有 SSH 推送部署已覆盖 user unit，但二进制直接位于安装根，更新只留一个 `.bak`。运营者无法永久保留每次部署版本，不能安全、明确地选择回滚版本；同机多 Worker 的更新路径还可能按错误 unit 操作。

目标是在不引入容器镜像或新运行进程的前提下，将 Linux 普通用户的 CP 与 Worker 部署升级为可迁移、不可变版本、可显式回滚的 user-systemd 直装模型。

## 2. 需求

- 保留 `scripts/deploy-cp.sh`、`scripts/deploy-worker.sh` 作为操作机 SSH 推送入口；新增 `scripts/rollback-cp.sh <版本目录>` 与 `scripts/rollback-worker.sh <版本目录>`。
- 仅 `JM_SERVICE_SCOPE=user` 使用本规格的版本化布局；既有 system scope 行为不变。
- 每次新部署将二进制落在永久保留的目录：`versions/<版本>--<UTC 时间戳>--<sha12>/`。同秒的相同制品若再次部署，在末尾追加 `-2`、`-3` 等序号而不覆盖已有目录。版本来自二进制 `--version`，摘要来自二进制 SHA-256；同目录保存完整摘要和安装时间 manifest。迁移不支持 `--version` 的历史裸二进制时，以 `unknown` 代替版本段并保留其摘要，绝不丢弃旧文件。
- `current` 是唯一运行指针，必须以同文件系统内的原子符号链接切换。unit 固定执行 `current` 下的二进制。
- 数据、CP 配置、JWT 环境文件、Worker 配置及节点身份稳定保留在版本目录外；回滚绝不删除或替换它们。
- 首次新版部署自动迁移旧安装根的裸二进制和已有 `.bak`，再将 user unit 改为 `current` 路径。迁移失败不得破坏原运行版本。
- CP 首装必须生成并 0600 保存非默认 JWT 密钥，后续部署不得覆盖；unit 经 `EnvironmentFile` 引用，不得把密钥写入命令行或日志。
- Worker 的 unit 名必须与安装目录一致；`jianmanager-worker` 和 `jianmanager-worker-node2` 等同机节点必须能独立更新、回滚和检查状态。
- 回滚只接受当前安装根 `versions/` 下的完整目录名；目标不存在、manifest 摘要不符、服务未恢复均须中文失败，并在最后一种情况恢复原 `current`。

### 范围外

- Docker Compose 生产部署、公开容器镜像、镜像仓库、Kubernetes、Helm。
- system scope 版本化迁移、批量多主机编排、版本自动清理。
- 数据库 schema、制品数据、Worker 身份的回滚。

## 3. 设计

### 3.1 稳定目录与版本目录

```text
<install-dir>/
├── current -> versions/<version>--<utc>--<sha12>/
├── versions/
│   └── <version>--<utc>--<sha12>[-<序号>]/
│       ├── jianmanager-cp 或 jianmanager-worker
│       └── manifest.env
├── data/
├── control-plane.yml              # 仅 CP
└── service.env                    # 仅 CP，0600
```

Worker 的 `worker.yml` 与 `etc/node-identity.json` 继续位于其稳定数据根。CP 的 SQLite、CAS 与自动生成密钥同样继续位于稳定数据根。`manifest.env` 仅包含版本、完整 SHA-256 和 UTC 安装时间，不含凭据。

### 3.2 部署、迁移与回滚

部署脚本在远端先校验传输后二进制摘要，再建版本目录和 manifest；成功后才切换 `current` 并重启 user unit。健康检查失败时恢复旧指针并重启。成功部署不删除任何版本。

检测到旧布局时，脚本先停止 unit，将根目录裸二进制及 `.bak` 分别归档到版本目录，保留稳定目录中的配置、数据和身份，再切换 unit 的 `ExecStart`。Worker 首装仍复用 `install-worker.sh` 的 setup/注册语义；该脚本需接受版本化二进制路径与由部署层解析的服务名，避免重建第二套上线流程。

回滚脚本以同一 `JM_*` SSH 配置连入远端，验证目标 manifest 和二进制摘要后原子切换 `current`、重启该安装目录派生的 user unit并检查 active。数据库升级可能不可逆，脚本必须明确说明它不回滚数据 schema。

### 3.3 服务与密钥

CP user unit 读取稳定根的 `service.env`。首次创建时脚本生成随机 JWT 密钥、设置 0600，并在 unit 使用 `EnvironmentFile`；已有文件永不覆盖。

Worker 单元名沿用 FR-282 的安装目录派生规则。部署、回滚和首次安装必须使用同一派生结果；同机多个安装根互不影响。

## 4. 任务拆分

- [x] 增加版本目录、manifest、摘要校验、原子 `current` 切换与回退的 POSIX shell 公共实现。
- [x] 改造 CP user 部署：首装 JWT 环境文件、旧布局迁移、版本化更新与健康失败恢复。
- [x] 改造 Worker user 部署与安装衔接：稳定服务名、旧布局迁移、版本化更新。
- [x] 新增 CP / Worker 回滚脚本。
- [x] 为脚本补迁移、摘要、回滚、服务名和 JWT 首装的模拟 SSH 测试。
- [x] 同步 `DEPLOY.md`、`ARCHITECTURE.md`、`README.md`、CHANGELOG 与 PRD 状态。
- [x] 在 `.env` 目标实测 CP、`jianmanager-worker` 和 `jianmanager-worker-node2` 的迁移、更新、回滚及数据/身份保留。

## 5. 验收标准

1. `sh -n`、脚本单测和模拟 SSH 覆盖 user 首装、旧布局迁移、重复部署、同机两个 Worker、目标不存在、摘要不符、健康失败恢复及显式回滚，全部通过。
2. 每次 user 部署产生唯一版本目录、完整 manifest 与正确摘要；既有版本永久保留，`current` 始终指向一个已验证目录。
3. 旧 CP / Worker 直装自动迁移后，CP 配置与 `data/` 内容、Worker `worker.yml` 与 `node-identity.json` 保留；同机两个 Worker unit 均按各自安装根操作。
4. CP 首装没有预设 JWT 时能启动；密钥只在 0600 环境文件中保存，后续部署不改变它。
5. 回滚指定存在版本后服务 active，运行二进制摘要与目标 manifest 一致；回滚失败时恢复原 `current`。
6. **真机（必须）**：使用 `.env` Linux 普通用户目标，依次验证 CP、`jianmanager-worker`、`jianmanager-worker-node2` 的迁移或版本化更新；每个组件至少一次回滚。检查 SSH 断开后 user unit 仍存活、稳定数据/配置/身份未改变，并记录证据到 `.tmp/`。

## 6. 风险与边界

- user unit 仍依赖 linger；无法启用时保持既有硬失败语义。
- 回滚不等于数据库 schema 回滚；需要先停止或备份数据的跨版本降级由运维自行评估。
- 正式版与开发版都按版本、UTC 时间和摘要命名，避免开发版本号重复覆盖。
- `.env` 目标已有两个 Worker，但当前均未运行；真机验收须先确认其保留身份能正常重连，再进行 ServerProbe 的后续全链路验收。
