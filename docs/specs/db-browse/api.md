# API Spec — FR-084 数据库资源管理器（只读浏览）

> 关联 FR: FR-084 | 优先级: P2 | 依赖: FR-070

## 概述

平台管理员在控制台「设置 → 数据库」页只读浏览 Control Plane 自身数据库（SQLite/MySQL，GORM）。
左栏列出全部表（资源管理器式表树），右栏分页 + 排序 + 简单过滤浏览选中表的行。

严守架构不变量「数据库仅 Control Plane 可读写」：本能力**只读**——仅 `SELECT`，无任何写/改/删端点。
敏感列（`password_hash` / `secret` / `token` / `node_secret` 等）一律服务端打码，原文不出后端。

复用方式：复用 FR-070 资源管理器**双栏布局/交互范式**（左树右内容），但因 ResourceExplorer 与文件 gRPC（`instanceId`）强耦合、
不可改其本体，故新建独立「数据库」页与轻量 `DatabaseExplorer` 组件承载表树 + 行表格，视觉与交互对齐资源管理器。

---

## 数据来源与边界

- 数据源 = Control Plane 进程持有的 `*gorm.DB`（`internal/controlplane/database`）。
- 表清单经 `db.Migrator().GetTables()`（GORM 跨 SQLite/MySQL 抽象，等价 information_schema/sqlite_master 列举）。
- 列清单经 `db.Migrator().ColumnTypes(table)`。
- 行查询用 GORM `db.Table(name)` 构造，表名/列名走**标识符白名单校验**（必须命中表/列清单），不拼接用户输入到 SQL；
  分页/排序用 GORM `Order/Limit/Offset`，过滤值作为参数化绑定（`?`），杜绝注入。

---

## REST API

> 均挂在 `admin` 路由组（`middleware.RequireRole(RolePlatformAdmin)`），Handler 内再以 `requirePlatformAdmin(c)` 兜底。

### GET /api/v1/db/tables

- **描述**: 列出 CP 数据库全部表及其行数估计
- **关联 FR**: FR-084
- **权限**: 平台管理员
- **响应** (200):
  ```json
  {
    "tables": [
      { "name": "users", "rowCount": 3 },
      { "name": "instances", "rowCount": 12 }
    ]
  }
  ```
- **错误**: 403 FORBIDDEN（非平台管理员）| 500 INTERNAL_ERROR（元数据读取失败）

### GET /api/v1/db/tables/:name/rows

- **描述**: 分页查询某表的行（含列定义），敏感列脱敏
- **关联 FR**: FR-084
- **权限**: 平台管理员
- **查询参数**:
  - `page` 页码，从 1 起，默认 1
  - `pageSize` 每页行数，默认 50，最大 200（越界钳制）
  - `sort` 排序列名（必须命中该表列；非法列忽略）
  - `order` `asc` | `desc`，默认 `asc`
  - `filterColumn` + `filterValue` 简单过滤：对该列做 `LIKE %value%`（列必须命中；二者需成对，否则忽略过滤）
- **响应** (200):
  ```json
  {
    "table": "users",
    "columns": [
      { "name": "id", "type": "integer", "sensitive": false },
      { "name": "username", "type": "text", "sensitive": false },
      { "name": "password_hash", "type": "text", "sensitive": true }
    ],
    "rows": [
      { "id": 1, "username": "admin", "password_hash": "******" }
    ],
    "page": 1,
    "pageSize": 50,
    "total": 3
  }
  ```
- **错误**:
  - 403 FORBIDDEN（非平台管理员）
  - 404 TABLE_NOT_FOUND（表名不在白名单）
  - 500 INTERNAL_ERROR（查询失败）

---

## 脱敏规则

列名（不区分大小写）匹配以下子串即判定敏感，值整体替换为 `******`（保留 `null`）：

```
password, passwd, secret, token, node_secret, private_key, priv_key, sign_priv,
salt, api_key, access_key, credential, pull_key, key_hash
```

> 命中即脱敏，宁可多打码不可漏。脱敏在服务端完成，原文不进入响应体。

---

## 前端

### 页面与导航

- 路由：`/database`，页面 `web/src/pages/DatabasePage.tsx`，在 `Workspace.tsx` 注册。
- 侧栏入口：`ConsoleSidebar` 的「设置」组追加 `{ to: '/database', labelKey: 'nav.database', icon: Database }`（图标已导入）。

### 交互（对齐 FR-070 资源管理器）

```
┌─────────────┬──────────────────────────────────────────────┐
│ 表树         │ [过滤列▼][过滤值____][清除]      共 3 行         │
│ ─ users (3)  │ ┌──────┬──────────┬────────────────┐          │
│   instances  │ │ id ▲ │ username │ password_hash  │          │
│   nodes …    │ ├──────┼──────────┼────────────────┤          │
│              │ │ 1    │ admin    │ ******         │          │
│              │ └──────┴──────────┴────────────────┘          │
│              │ « 上一页   第 1 页 / 共 1 页   下一页 »          │
└─────────────┴──────────────────────────────────────────────┘
```

- 左：表树（点选切换当前表，显示行数）。
- 右：表头列名 + 点击列头切换排序（asc/desc）；顶部过滤条（选列 + 关键字）；底部分页器。
- 大表分页不卡：仅请求当前页（`page`/`pageSize`），切表/翻页/排序/过滤均走后端，不一次性拉全表。
- 只读：无任何编辑/删除入口。

### API hook

`web/src/api/db.ts`，TanStack Query：
- `useDbTables()` → `GET /db/tables`
- `useDbTableRows(table, params)` → `GET /db/tables/:name/rows`（`queryKey` 含 table + 分页/排序/过滤，`enabled: !!table`，`keepPreviousData` 翻页不闪）

---

## 错误处理

| 场景 | 表现 |
|---|---|
| 非平台管理员 | 后端 403；前端页面由侧栏入口可见性 + 接口 403 兜底 |
| 表不存在/非法表名 | 后端 404 TABLE_NOT_FOUND |
| 非法排序/过滤列 | 后端静默忽略该参数（不报错，回退默认） |
| 元数据/查询失败 | 后端 500；前端 toast/占位错误文案 |
| 空表 | 右栏显示「无数据」，分页 total=0 |
