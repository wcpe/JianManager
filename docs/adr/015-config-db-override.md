# ADR-015: 平台配置 DB 覆盖层

- **日期**: 2026-06-22
- **状态**: accepted
- **上下文**: 平台配置（FR-063）此前只有两个来源——`configs/control-plane.yaml`（server/grpc/database/jwt/log/log_store/file_version）与若干环境变量（`JIANMANAGER_JDK_{TEMURIN,CORRETTO,ZULU}_BASE`、`JIANMANAGER_GRACEFUL_STOP_TIMEOUT` 等）。这套 YAML+env 基线由 ADR-005（go:embed 单二进制）与 `.claude/rules/config-files.md`（YAML 格式、`${ENV_VAR}` 引用敏感值、零配置可启动）约束，运维要调一个值必须改文件 + 重启进程。FR-063 要求把全部平台配置在 Web 设置页可视化，并让「可安全运行时调整」的项改完即生效（无需重启），同时启动固定/敏感项只读展示。问题是：**配置从哪儿来、谁覆盖谁、哪些能运行时改、敏感值怎么处理**，需要一个明确的优先级与边界，否则会出现「文件、环境、DB 三处打架」与「敏感值明文下发前端」的风险。
- **决策**:
  1. **新增 DB 覆盖层 `platform_settings`（key/value）**：在既有 YAML+env 之上叠加一层运行时覆盖。表结构为 `key varchar(128) 主键 + value text + updated_at`，只存「被显式覆盖过的键」，未覆盖的键不落库。
  2. **生效优先级固定为 DB 覆盖 > 环境变量 > YAML 默认**：解析「有效配置」时，先取 `config.Load`（已含 YAML+env+内置默认）得到基线，再用 `platform_settings` 中存在的键逐项覆盖。DB 没有的键完全沿用基线，等价于现状。
  3. **可写白名单（运行时可调）封闭枚举**：仅以下键允许经 `PUT /settings` 落库覆盖——`log.level`（日志级别）、`jdk.mirror.temurin` / `jdk.mirror.corretto` / `jdk.mirror.zulu`（JDK 下载镜像源）、`graceful_stop.timeout`（优雅停止超时）、`backup.retention_days`（默认备份保留天数）。白名单之外的键一律拒绝写入（422），杜绝把启动固定项/敏感项改坏。
  4. **只读项只展示不入库**：server host/port、gRPC 端口、database driver/dsn、jwt secret、jwt access/refresh TTL 等启动固定项，`GET /settings` 返回当前生效值供查看，但不接受写入，并标注「需改配置并重启」。
  5. **敏感值脱敏下发**：`jwt.secret` 与 `database.dsn` 中的口令片段在 `GET /settings` 中打码（如 `dev-***-me`），不返回明文；`PUT /settings` 也不接受这两项。
  6. **运行时生效以「有真实读取点」为界**：能在 Control Plane 进程内直接接到读取点的（日志级别——slog `LevelVar` 动态改档），落库即时生效；读取点在 Worker 进程的（JDK 镜像源、优雅停止超时经 env 在 Worker 侧消费）与当前无消费者的（备份保留天数尚无裁剪逻辑），CP 负责存储 + API + 前端展示，生效链路与接入点在 FR-063 实现说明中逐项写明，不谎称即时全链路生效。
- **理由**:
  - 覆盖层而非「迁移到 DB」：YAML/env 仍是**基线与可移植部署契约**（裸机/容器、首次启动、灾备恢复都只依赖文件 + 环境），DB 仅承载「运维在运行期临时改」的少量白名单项。这样既满足 FR-063 的运行时可调，又不破坏 ADR-005 的单二进制自包含与 `config-files.md` 的零配置可启动。
  - 优先级取 `DB > env > YAML`：DB 是「运维显式、最新、就近」的意图，理应最高；env 次之（部署期注入）；YAML 默认兜底。三层单调覆盖、无环路，解析逻辑简单可测。
  - 白名单封闭：把「能运行时改」限定在已知安全的少数键，避免运维误改端口/密钥/DSN 导致进程行为漂移或自锁。
  - 敏感值不出库不出网：遵守 `config-files.md`「敏感信息不硬编码、不明文」，设置页能看到「有没有配、配成什么形态」但看不到明文。
- **后果**:
  - 新增 `platform_settings` 表（AutoMigrate 注册）、`SettingsService`（读写 + 有效配置解析 + 脱敏）、`GET /settings` / `PUT /settings`（仅平台管理员）。须同步 `docs/API.md`、`docs/ARCHITECTURE.md` 数据库模型章节。
  - CP 日志器由静态 `HandlerOptions.Level` 改为 `*slog.LevelVar`，使 `log.level` 可运行时切换；这是行为等价的内部重构，不改默认级别语义。
  - JDK 镜像源、优雅停止超时的「跨进程即时生效」留作后续（需扩展 gRPC 下发或 Worker 侧读取 CP 覆盖），本 ADR 不引入 proto 变更；当前这两项与备份保留天数为「CP 存储 + 展示」，env 仍是 Worker 侧实际生效途径。
- **对既有决策的影响（论证不冲突）**:
  - **ADR-005（go:embed 单二进制）**：不冲突。未引入外部配置中心或额外进程；覆盖层就在 CP 自己的 SQLite/MySQL 内，仍是单二进制自包含。无 DB（首次启动）时 `platform_settings` 为空，行为与今天完全一致。
  - **`.claude/rules/config-files.md`**：不冲突。YAML 仍是唯一配置文件格式与基线；`${ENV_VAR}` 引用敏感值的约定不变；「配置项必须在代码中有对应 struct」不变（DB 覆盖的是同一批 struct 字段的有效值，不是另立配置体系）；「零配置可启动」不变（DB 覆盖是可选增量）。本 ADR 只是在「文件 + 环境」之上加了一层**可选的、白名单受限的、运行时覆盖**，没有把任何敏感值落明文，也没有让「文件里有但代码不读」的孤儿配置出现。
- **替代方案**:
  - **全量配置迁移进 DB、YAML 仅做引导** — 破坏可移植部署契约（灾备/容器首启需要先有 DB 才能跑），与 ADR-005 相悖，否决。
  - **直接热重载 YAML 文件（fsnotify）** — 运维仍需登机器改文件，无法在 Web 上管，且多副本/容器不可写文件系统场景失效，不满足 FR-063，否决。
  - **不分白名单、任意键可改** — 运维易误改端口/密钥导致自锁或安全降级，否决。
  - **外部配置中心（etcd/Consul/Nacos）** — 与单二进制自包含相悖，运维负担重，否决。
