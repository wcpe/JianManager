# 功能规格：平台存储资源管理器

> 状态：已交付@v0.13.0　·　关联 PRD：FR-083 / FR-044 / FR-045　·　分支：dev

## 1. 背景与目标

JianManager 已通过 FR-044 建立项目自包含便携运行时与 FHS 数据根，通过 FR-045 建立制品库及冷热/归档状态，但平台管理员仍缺少一个只读入口来回答：

- Control Plane 当前数据根实际落在哪里，FHS 子目录是否完整。
- `bin/`、`etc/`、`opt/jdks/`、`var/servers/`、`var/log/`、`var/artifacts/`、`cache/` 分别占用多少空间与文件数。
- 制品库热数据、归档数据、外置数据的数量与占用如何分布。
- `cache/` 是否可以安全受控清理，清理后是否确实只影响缓存目录。

FR-083 是 FR-044 的平台侧收口能力：它只面向 Control Plane 本机数据根，不跨进程读取 Worker 本地目录，不新增表或协议。

## 2. 需求（要什么）

- 提供「平台存储」页面，路由为 `/storage`，入口位于控制台「系统 / 平台与维护」。
- 页面仅平台管理员可用；普通成员不可看到数据根路径、目录占用、文件列表或归档分布。
- 展示 Control Plane 数据根绝对路径，仅用于运维排查，不提供下载或内容读取。
- 展示固定 FHS 目录清单：
  - `bin`
  - `etc`
  - `opt/jdks`
  - `var/servers`
  - `var/log`
  - `var/artifacts`
  - `cache`
- 每个目录展示：用途、相对路径、递归大小、递归文件数、是否实际存在、是否允许清理。
- 缺失但属于 FHS 布局的目录仍需列出，标记 `exists=false`，避免运营误以为空间统计遗漏。
- 制品归档冷热分布展示：
  - `hot` 数量与大小。
  - `archived` 数量与大小。
  - `external` 数量与大小。
- 数据根浏览器只列举目录直接子项：目录在前、同类按名称排序；允许点击目录下钻；不读取文件内容。
- 路径参数必须限制在数据根内：`..`、系统绝对路径、折叠后逃逸路径必须拒绝。
- `cache/` 是唯一允许受控清理的目录：
  - 前端必须走二次确认。
  - 后端只删除 `cache/` 下直接子项。
  - 保留 `cache/` 目录本身。
  - 不得触碰 `cache/` 之外的任何目录。

### 范围内

- 后端平台存储只读聚合与受控清理端点。
- 前端 `/storage` 页面、占用统计、归档分布、只读目录浏览、cache 二次确认清理。
- mock 假后端与自动化测试覆盖权限、浏览、路径守卫、清理联动。
- PRD、ARCHITECTURE、API、CHANGELOG 同步。

### 范围外

- 不读取文件内容，不做预览、下载、编辑、删除任意文件。
- 不浏览 Worker 节点本机数据根；Worker 侧实例文件仍走既有实例文件管理 / gRPC 能力。
- 不新增实例 ↔ 制品引用模型；归档分布仅来自 `assets.storage_state` 与 `assets.size`。
- 不改变 FR-044/FR-045 既有目录布局与制品库模型。

## 3. 设计（怎么做）

### 3.1 后端

- 新增/使用 `StorageService` 聚合 Control Plane 数据根与 `assets` 表。
- 数据根来源为 `dataroot.Root`，所有物理路径必须经数据根解析。
- `Overview()`：
  - 按固定 FHS 目录顺序递归统计大小与文件数。
  - 缺失目录返回 `exists=false` 且大小/文件数为 0。
  - 从 `assets` 表批量读取 `size` 与 `storage_state`，聚合冷热/归档/外置分布。
- `List(path)`：
  - 空路径表示数据根。
  - 只返回直接子项，不递归。
  - 返回项包含 `name/isDir/size/modTime`。
  - 排序规则：目录在前，再按名称升序。
  - 只允许目录路径；文件路径返回 `NOT_A_DIR`。
- `ClearCache()`：
  - 固定操作 `cache/`。
  - 缺失或空缓存目录幂等返回 0。
  - 删除 `cache/` 的直接子项；若子项是目录，可递归删除该子目录。
  - 不接受任意路径参数。
- REST 端点：
  - `GET /api/v1/storage/overview`
  - `GET /api/v1/storage/files?path=`
  - `POST /api/v1/storage/cache/clear`
- 权限：全部端点仅平台管理员；未登录返回 401，已登录但非平台管理员返回 403。

### 3.2 前端

- 页面 `StoragePage`：
  - 读取 `/storage/overview`。
  - 非平台管理员前端兜底显示无权限，不发起数据展示。
  - 显示数据根路径、总大小、总文件数、目录数、冷数据摘要。
  - 目录占用区按占用突出展示，缺失目录置灰。
  - 制品归档区展示 `hot/archived/external` 三类数量和大小。
  - 只读浏览区通过 `/storage/files` 下钻目录。
- `cache/` 清理按钮：
  - 仅当后端 `clearable=true` 且文件数大于 0 时可点。
  - 使用 `DangerConfirm` 二次确认。
  - 成功后失效 `['storage', 'overview']` 与 `['storage', 'files']` 查询，触发页面联动刷新。
- 展示逻辑保持在 `storage-view.ts`：字节格式化、归档派生、目录排序、面包屑、路径拼接。

### 3.3 Mock 与测试数据

- mock 假后端 `/storage/*` 必须镜像后端平台管理员权限，不得只校验登录。
- mock FHS seed 必须覆盖固定目录：`bin`、`etc`、`opt/jdks`、`var/servers`、`var/log`、`var/artifacts`、`cache`。
- mock cache 清理后必须联动：
  - `cache` 目录大小和文件数归零。
  - `storage/files?path=cache` 不再返回缓存文件。
  - overview 总大小/总文件数随之变化。

## 4. 任务拆分

- [x] 补齐本规格并经审核通过。
- [x] 对齐 mock `/storage/*` 权限为平台管理员，并补齐固定 FHS seed。
- [x] 补充 StoragePage DOM 测试：普通成员拒绝、固定 FHS 目录、cache 清理联动。
- [x] 复核后端 service/router 测试：路径逃逸、非目录、缺失目录、cache 清理、未登录 401、普通成员 403。
- [x] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG。
- [x] 真机验收：mock dev 页面展示、目录下钻、cache 清理后联动刷新、普通成员不泄露数据。

## 5. 验收标准

- 平台管理员访问 `/storage` 可看到数据根路径、总占用、总文件数、固定 FHS 子目录、制品归档冷热分布。
- 普通成员访问页面或 `/api/v1/storage/*` 均不能获得数据根路径、目录占用、文件列表或归档分布；接口返回 403。
- 未登录访问 `/api/v1/storage/*` 返回 401。
- `GET /storage/overview` 固定返回 FHS 目录清单；缺失目录也列出且标记 `exists=false`。
- `GET /storage/files?path=` 仅列出直接子项，不读取文件内容，目录在前并按名称排序。
- 越界路径被拒绝：`..`、折叠后逃逸、系统绝对路径不得读到数据根外。
- `POST /storage/cache/clear` 只能清理 `cache/` 直接子项，保留 `cache/` 本身，不影响 `var/artifacts` 等其它目录。
- 前端清理 cache 前必须二次确认；成功后概览和浏览列表联动刷新。
- 自动化验证：
  - `go test ./internal/controlplane/service -run Storage`
  - `go test ./internal/controlplane/router -run Storage`
  - `npm --prefix web run test:node -- storage-view`
  - `npm --prefix web run test:dom -- StoragePage.dom.test.tsx`
- 真机/浏览器验收：mock dev 中以管理员打开 `/storage` 可浏览根目录并清理 cache；以普通成员访问时不展示平台存储数据。

## 6. 风险 / 待定

- 数据根绝对路径本身属于平台运维信息，仅平台管理员可见；普通成员不得通过 mock 或前端缓存看到。
- 目录递归统计在超大数据根上可能耗时；本期按固定少量 FHS 子目录统计，不在请求循环中追加数据库查询。若后续需要异步后台统计，应另立 FR。
- 本页不提供文件内容读取，避免把配置、密钥、数据库文件等敏感内容通过平台存储页泄露。
