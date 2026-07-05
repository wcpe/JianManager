# 功能规格：数据库资源管理器

> 状态：已交付@v0.13.0　·　关联 PRD：FR-084　·　分支：dev

## 1. 背景与目标

JianManager 的数据库由 Control Plane 独占读写。运营与排障时，平台管理员需要只读查看当前数据库表、列与部分行数据，以确认配置、实例、节点、任务、审计等平台状态是否符合预期。

FR-084 提供数据库资源管理器：在不破坏「数据库仅 Control Plane 可读写」架构不变量的前提下，给平台管理员一个受控、只读、分页、脱敏的数据库浏览入口。

现有 API 细节文档为 `docs/specs/db-browse/api.md`。本文件作为完整功能规格，定义范围、设计、任务与验收标准；`api.md` 保留为接口参数与响应细节参考。

## 2. 需求（要什么）

- 提供「数据库」页面，路由为 `/database`，入口位于控制台「系统 / 平台与维护」。
- 页面仅平台管理员可访问；普通成员不可看到表名、行数、列名或行数据。
- 左侧以资源管理器式表树展示 Control Plane 数据库表清单与行数。
- 右侧只读展示选中表的列定义与当前页行数据。
- 支持分页：只请求当前页，不一次性拉取整表。
- 支持点击列头排序：排序列必须存在于当前表列白名单内。
- 支持简单过滤：过滤列必须存在于当前表列白名单内，过滤值作为参数绑定，不拼接 SQL。
- 敏感列必须由服务端脱敏后再返回；前端仅做兜底重复打码。
- 不提供任何创建、更新、删除、导出、下载、执行 SQL 的能力。

### 范围内

- 后端表清单端点与分页行查询端点。
- 表名、列名白名单校验。
- 敏感列识别与服务端脱敏。
- 前端数据库浏览页面、表树、分页、排序、过滤、错误态与无权限兜底。
- mock 假后端与自动化测试覆盖权限、脱敏、分页、排序、过滤。
- PRD、ARCHITECTURE、API、CHANGELOG 同步。

### 范围外

- 不执行用户输入的原始 SQL。
- 不提供写操作、批量修改、删除、清表、迁移、导入、导出。
- 不直接连接 Worker 或 Bot 进程。
- 不提供跨数据库连接管理；数据源仅为当前 Control Plane 持有的 `*gorm.DB`。
- 不绕过 GORM migrator/白名单直接拼接任意用户输入到 SQL。

## 3. 设计（怎么做）

### 3.1 后端

- 使用 `DBBrowseService` 承载只读元数据与行查询。
- 数据源为 Control Plane 当前进程持有的 `*gorm.DB`。
- `Tables()`：
  - 通过 `db.Migrator().GetTables()` 获取表名。
  - 按表名稳定排序。
  - 对每张表执行 count，失败时该表行数记为 `-1`，不使整个表清单失败。
- `TableRows(table, params)`：
  - 表名必须命中 `Migrator().HasTable(table)`，否则返回 `TABLE_NOT_FOUND`。
  - 列定义来自 `Migrator().ColumnTypes(table)`。
  - 排序列与过滤列必须命中列白名单；非法列静默忽略，回退默认查询。
  - 分页参数钳制：默认 `pageSize=50`，最大 `200`。
  - 过滤值使用参数化绑定，禁止把用户输入拼入 SQL。
  - 查询结果中的敏感列服务端统一替换为 `******`，`null` 保持 `null`。
- 敏感列判定：列名不区分大小写，包含以下任一片段则脱敏：
  - `password`
  - `passwd`
  - `secret`
  - `token`
  - `node_secret`
  - `private_key`
  - `priv_key`
  - `sign_priv`
  - `salt`
  - `api_key`
  - `access_key`
  - `credential`
  - `pull_key`
  - `key_hash`
- REST 端点：
  - `GET /api/v1/db/tables`
  - `GET /api/v1/db/tables/:name/rows`
- 权限：全部端点仅平台管理员；未登录返回 401，已登录但非平台管理员返回 403。

### 3.2 前端

- 页面 `DatabasePage`：
  - 根据 auth store role 做平台管理员兜底；非管理员显示无权限占位。
  - 管理员渲染 `DatabaseExplorer`。
- `DatabaseExplorer`：
  - 左栏读取 `/db/tables` 并显示表名与行数。
  - 默认选中首个表。
  - 切表后右栏状态重置。
  - 右栏读取 `/db/tables/:name/rows`，query key 包含表名、分页、排序、过滤参数。
  - 点击列头在未排序、升序、降序、取消排序之间切换。
  - 过滤条包含过滤列选择、过滤值输入、应用与清除。
  - 分页器支持每页 25 / 50 / 100 / 200。
  - 敏感列前端再次兜底显示 `******`，即使后端返回异常原值也不直接展示。
- 只读约束：页面不得出现编辑、删除、保存、执行 SQL、导出等操作入口。

### 3.3 Mock 与测试数据

- mock 假后端 `/db/*` 必须镜像后端平台管理员权限，不得只校验登录。
- mock 表 seed 至少覆盖：
  - `users`：包含 `admin/operator` 和敏感密码列，验证脱敏。
  - `instances`：包含实例行，验证切表。
- mock 行查询必须支持：分页、排序、过滤、敏感列脱敏、未知表错误。
- 未知表错误码应与后端一致：`TABLE_NOT_FOUND`。

## 4. 任务拆分

- [x] 补齐本规格并经审核通过；保留 `api.md` 作为接口细节文档。
- [x] 对齐 mock `/db/*` 权限为平台管理员，错误码对齐后端。
- [x] 补充 DatabasePage DOM 测试：普通成员拒绝、不泄露表数据、过滤/排序交互。
- [x] 复核后端 service/router 测试：未登录 401、普通成员 403、表白名单、列白名单、分页、排序、过滤、敏感列脱敏。
- [x] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG。
- [x] 真机验收：mock dev 中管理员可浏览表与分页行，普通成员无法访问数据库数据。

## 5. 验收标准

- 平台管理员访问 `/database` 可看到表清单、行数、列头与当前页行数据。
- 普通成员访问页面或 `/api/v1/db/*` 均不能获得表名、行数、列名或行数据；接口返回 403。
- 未登录访问 `/api/v1/db/*` 返回 401。
- `GET /db/tables` 返回稳定排序的表清单和行数；单表 count 失败不导致整页不可用。
- `GET /db/tables/:name/rows` 支持分页，并将 `pageSize` 限制在最大 200。
- 排序列和过滤列必须通过列白名单；非法列不得造成 SQL 注入或 500。
- 未知表返回 404 `TABLE_NOT_FOUND`。
- 敏感列在服务端响应中已脱敏；前端也不会展示敏感列原值。
- 页面无任何写操作入口。
- 自动化验证：
  - `go test ./internal/controlplane/service -run DBBrowse`
  - `go test ./internal/controlplane/router -run DBBrowse`
  - `npm --prefix web run test:node -- rows-view`
  - `npm --prefix web run test:dom -- DatabasePage.dom.test.tsx`
- 真机/浏览器验收：mock dev 中以管理员打开 `/database` 可切换表、排序、过滤；以普通成员访问时不展示数据库表或行。

## 6. 风险 / 待定

- 数据库表名、列名和行数据均可能包含敏感运维信息，必须严格限制为平台管理员。
- 本能力只读且不执行原始 SQL，避免变成数据库控制台。
- 大表浏览必须分页；不得一次性加载全表到内存或前端。
- SQLite 与 MySQL 的标识符规则存在差异，排序/过滤列必须来自 GORM migrator 白名单，后续若支持更多数据库方言需复核 `quoteIdent` 策略。
