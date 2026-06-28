# 功能规格：通知中心（站内信 + 告警合并为统一通知流）

> 状态：开发中　·　关联 PRD：FR-216　·　ADR：[ADR-048](../../adr/048-unified-notification-model.md)　·　分支：feat/fr-216-notification-center

## 1. 背景与目标

平台此前「给人看的通知」有两套独立来源、页眉**两个入口**：

- **站内信**（`notifications`，ADR-040/FR-183）：投递给某用户的**定向消息**（任务完成/失败等），页眉「收件箱」入口（`NotificationInbox`）。
- **告警事件**（`alert_events`，FR-011/FR-085）：告警引擎触发的**系统警报**，页眉「告警铃铛」入口（`AlertBell`，链 `/alerts`）+ 侧栏观测域过渡「告警」位（FR-215 留位，明示由本 FR 接手）。

本 FR 把二者合并为**统一通知流**：**页眉单铃铛**（下拉预览混合未读）+ 独立**「通知中心」页**（按类型筛选[消息/告警]、标记已读、关键字查询、分页）+ 侧栏入口收口到**「系统/账户与审计」**。统一只做在**读侧**（聚合服务 + 统一端点），两套写入源与既有端点保留（见 ADR-048）。属 P1，是观测重构批次波 2 一项，依赖 FR-215 落点。

## 2. 需求（要什么）

### 范围内

1. **后端统一查询**：新增只读聚合，把 `notifications`（按当前用户）+ `alert_events`（全局）合并为一条通知流，带 `source` 区分（`message`/`alert`）、`source` 筛选、`unread` 筛选、`keyword` 模糊、分页；统一未读计数（用户站内信未读 + 全局告警未读）；统一标记已读（单条按 source 下推、全部已读）。
2. **页眉单铃铛**：移除原 `AlertBell` + `NotificationInbox` 两入口，合并为一个「通知」铃铛——未读角标（轮询）+ 下拉预览最近若干条混合通知（消息/告警各带来源标识与级别色点）+「查看全部」跳通知中心。
3. **通知中心页**（`/notifications`，新建 `NotificationCenterPage`）：
   - 类型筛选：全部 / 消息 / 告警（`source`）。
   - 仅未读切换。
   - 关键字查询（标题/正文）。
   - 列表分页（与告警事件页同款分页交互）。
   - 行内「标记已读」+ 顶部「全部已读」。
   - 告警条目提供「查看告警详情」入口（跳 `/alerts`，深一步的确认/认领仍在告警页）。
4. **侧栏收口**：移除观测域的「告警」过渡留位（FR-215 注释交接）；在「系统 · 账户与审计」小节新增「通知中心」入口（`/notifications`）。**观测域不再有独立告警项**。
5. **breadcrumb**：新增 `/notifications` 段映射到「系统 › 通知中心」；`/alerts` 段归属从观测域调整为系统域（与侧栏一致，避免孤儿域归属）。
6. **i18n**：zh/en 同步新增通知中心相关键（筛选/来源/查询/分页/空态）。
7. **mock**：MSW 假后端补统一 feed 端点（聚合既有 notifications + alertEvents 集合），双形态/测试可消费。

### 不做（范围外）

- **不动告警规则/通道/事件管理页 `/alerts`**（`AlertsPage`）本身的功能：规则增删改、通道、事件确认/认领仍在该页，**仅其侧栏入口归属与页眉铃铛**变化。`/alerts` 路由保留。
- **不新建统一物理表、不双写、不回填**（ADR-048：视图聚合）。
- **不改告警引擎 / 站内信投递写入路径**。
- **不删既有 `/notifications/*`、`/alerts/*` 端点**（向后兼容）。
- **不做通知偏好/订阅设置**（哪些类型进站内）——非本 FR 目标，记 backlog。

## 3. 设计（怎么做）

### 3.1 后端

**模型**：无新表（ADR-048）。复用 `model.Notification` + `model.AlertEvent`。

**服务** `internal/controlplane/service/notification_feed.go` 新增 `NotificationFeedService`：

```go
type FeedSource string // "message" | "alert"

type FeedItem struct {
    Source       FeedSource // 来源判别
    ID           uint       // 源表主键（同 source 内唯一）
    Level        string     // 统一枚举 info/success/warning/error
    Title        string
    Body         string
    Read         bool
    CreatedAt    time.Time  // 统一排序键（message=created_at, alert=fired_at）
    TaskID       string     // 仅 message
    TriggerType  string     // 仅 alert
    Acknowledged bool       // 仅 alert
    Resolved     bool       // 仅 alert
}

type FeedFilter struct {
    Source   string // ""=全部 / message / alert
    Unread   bool
    Keyword  string
    Page     int
    PageSize int
}

func (s *NotificationFeedService) Feed(userID uint, f FeedFilter) (items []FeedItem, total int64, err error)
func (s *NotificationFeedService) UnreadCount(userID uint) (int64, error)
func (s *NotificationFeedService) MarkRead(userID uint, source string, id uint) error
func (s *NotificationFeedService) MarkAllRead(userID uint) (int64, error)
```

实现要点：

- `Feed`：按 `Source` 决定查哪一/两源。
  - message 源：`notifications WHERE user_id=? [AND read_at IS NULL] [AND (title LIKE ? OR body LIKE ?)]`，按 `created_at DESC` 取上界（`page*pageSize`）条 + 各自 count。
  - alert 源：`alert_events [WHERE read=0] [AND message LIKE ?]`，预加载 Rule 取名，按 `fired_at DESC` 取上界条 + count。级别映射 warn→warning/critical→error。
  - 合并：两源 candidate 列表归并按 `CreatedAt DESC`，切 `[(page-1)*pageSize, page*pageSize)`；`total = messageTotal + alertTotal`。
  - keyword 同时作用两源；source 限定时只查该源。
- `UnreadCount`：`notifications(user_id, read_at IS NULL) count + alert_events(read=false) count`。
- `MarkRead(source,id)`：`message`→委托 `NotificationService.MarkRead(userID,id)`（归属校验）；`alert`→委托 `AlertService.MarkRead(id)`。非法 source 返回错误。
- `MarkAllRead`：站内信按 userID 全读 + 告警全局全读，返回受影响合计。

**路由** `internal/controlplane/router/notification.go`（新文件，把 feed 端点与 NotificationHandler 解耦，原 NotificationHandler 留在 task.go 不动）：注册到认证用户组（`protected`），归属隔离在 service 收敛。

| 方法 | 路径 | 描述 | 权限 |
|---|---|---|---|
| GET | `/api/v1/notifications/feed` | 统一通知流分页（Query: `source`,`unread`,`keyword`,`page`,`pageSize`） | 认证用户（消息按本人，告警全局） |
| GET | `/api/v1/notifications/feed/unread-count` | 统一未读数（本人站内信未读 + 全局告警未读） | 认证用户 |
| POST | `/api/v1/notifications/feed/read-all` | 全部标记已读（站内信本人 + 告警全局） | 认证用户 |
| POST | `/api/v1/notifications/feed/:source/:id/read` | 标记单条已读（source=message/alert） | 认证用户（message 仅本人） |

**错误码**：`400 INVALID_REQUEST`（source 非法）；`404 NOT_FOUND`（message 标记已读时不存在/非本人，由 NotificationService 既有逻辑产出）；`401 UNAUTHORIZED`（未认证）。

**响应体**：

- `GET /notifications/feed` → `{ "items": [FeedItem...], "total": <int> }`，FeedItem JSON：`{ source, id, level, title, body, read, createdAt, taskId?, triggerType?, acknowledged?, resolved? }`。
- `GET /notifications/feed/unread-count` → `{ "unread": <int> }`。
- `POST .../read-all` → `{ "updated": <int> }`。
- `POST .../:source/:id/read` → `{ "message": "已标记已读" }`。

接线：`router.go` 在 `svcs.Notification != nil && svcs.Alert != nil` 时构造 `NewNotificationFeedService(svcs.Notification, svcs.Alert, db)` 注册（需在 `Services` 暴露 feed 所需依赖；复用既有 `Notification`/`Alert` 服务 + `db`）。

### 3.2 前端

**API** `web/src/api/notification-feed.ts`（新）：`FeedSource`/`FeedItem`/`FeedQuery` 类型 + hooks `useNotificationFeed(query)`、`useFeedUnreadCount()`（轮询）、`useMarkFeedRead()`、`useMarkAllFeedRead()`。queryKey 前缀 `notificationFeed`，标记已读后失效 feed + unread-count。

**页眉**（`ConsoleHeader.tsx`）：

- 删 `InboxSlot`（`NotificationInbox`）与 `AlertBell` 两槽，合并为单 `NotificationBell`：铃铛 + 未读角标（`useFeedUnreadCount`）+ 下拉预览最近 ~8 条（`useNotificationFeed({pageSize:8})`），每条带来源标识（消息/告警）+ 级别色点 + 未读点；底部「标记全部已读」+「查看全部」→ `navigate('/notifications')`。
- `header-layout.ts`：槽位 `inbox` + `alertBell` 合并为单 `notifications`（`always` 可见）；更新 `HEADER_RIGHT_SLOTS` 与 `slotVisibility` 穷尽、调整其 vitest。

**通知中心页** `web/src/pages/NotificationCenterPage.tsx`（新，路由 `/notifications`）：

- 顶部：标题 + 类型筛选（全部/消息/告警，pill 或 select）+ 仅未读切换 + 关键字输入 + 「全部已读」按钮。
- 列表：`Panel` + 行（来源徽标 + 级别 `StatusBadge` + 标题 + 正文截断 + 时间 + 未读高亮 + 行内「标记已读」；告警条目附「查看详情」链 `/alerts`）。复用 `StatusBadge`/`Panel` 原语，遵循 ui-modals/卡片范式（本页无新增弹窗）。
- 分页：与 `AlertsPage` 事件页同款（上一页/下一页 + 第 X/Y 页 + 总数）。

**路由**（`Workspace.tsx`）：新增 `const NotificationCenterPage = lazy(...)` + `<Route path="notifications" element={<NotificationCenterPage />} />`。`/alerts` 路由保留不动。

**侧栏**（`ConsoleSidebar.tsx`）：

- 观测域 `children` 删除 `{ to: '/alerts', ... }` 过渡留位（连同 FR-215 注释）。
- 「系统 · 账户与审计」小节新增 `{ to: '/notifications', labelKey: 'nav.notifications', icon: Bell }`（置于设置/审计附近；账户与审计语义=「与我账户相关的提醒/审计」，通知中心归此）。

**breadcrumb**（`lib/breadcrumb.ts`）：

- `SEGMENT_DOMAIN`：新增 `notifications: 'nav.system'`；`alerts` 从 `nav.observability` 改为 `nav.system`（与侧栏一致——告警管理页归系统域）。
- `SEGMENT_PAGE`：新增 `notifications: 'nav.notifications'`（`alerts` 仍 `nav.alerts`）。

### 3.3 i18n（zh/en）

- `nav.notifications`：通知中心 / Notifications
- `notificationCenter.title`：通知中心 / Notification Center
- `notificationCenter.sourceAll` / `sourceMessage` / `sourceAlert`：全部 / 消息 / 告警（All / Messages / Alerts）
- `notificationCenter.onlyUnread`：仅未读 / Unread only
- `notificationCenter.keywordPlaceholder`：搜索标题或内容… / Search title or body…
- `notificationCenter.markAllRead` / `markRead`：全部已读 / 标记已读
- `notificationCenter.empty`：暂无通知 / No notifications
- `notificationCenter.viewAlertDetail`：查看告警详情 / View alert detail
- `notificationCenter.badgeMessage` / `badgeAlert`：消息 / 告警（Message / Alert）
- `notificationCenter.total` / `prevPage` / `nextPage` / `pageOf`：复用同款分页文案（或独立键）
- 页眉 `header.notifications`：通知 / Notifications；`header.viewAllNotifications`：查看全部 / View all

> 既有 `header.alerts`/`viewAllAlerts`/`noAlerts`、`notifications.*`（站内信收件箱）键**保留**（AlertsPage/旧组件可能仍引用）；本 FR 不删旧键，仅新增。

### 3.4 mock（`web/src/mocks/handlers/domains/observ.ts`）

新增统一 feed 端点，复用既有 `notifications` + `alertEvents` 集合：

- `GET /notifications/feed`：合并两集合为 FeedItem（message←notifications，alert←alertEvents 映射），按 `source`/`unread`/`keyword` 过滤、按时间倒序、`page`/`pageSize` 切片，返回 `{items,total}`。
- `GET /notifications/feed/unread-count`：`notifications(未读) + alertEvents(未读) count`。
- `POST /notifications/feed/read-all`：两集合全标记已读。
- `POST /notifications/feed/:source/:id/read`：按 source 更新对应集合该条已读。

## 4. 任务拆分

- [x] 写 ADR-048（统一通知模型）+ 本 spec
- [x] 后端 `NotificationFeedService` + 单测（合并/筛选/未读/标记已读/归属隔离/级别映射）
- [x] 后端路由 `notification.go` feed 端点 + router.go 接线
- [x] 前端 `api/notification-feed.ts`
- [x] 页眉合并单铃铛（`ConsoleHeader.tsx` + `header-layout.ts` + 其 test）
- [x] 通知中心页 `NotificationCenterPage.tsx` + 路由
- [x] 侧栏收口（删告警过渡位 + 加通知中心入口） + breadcrumb + pageTitle
- [x] i18n zh/en
- [x] mock feed 端点
- [x] vitest（页眉单铃铛、通知中心页筛选/已读、侧栏 IA 收口、breadcrumb）
- [x] 文档同步：API.md + ARCHITECTURE（数据模型/导航） + PRD FR-216 状态
- [ ] 真机验收（需用户确认，标「待真机验」）

## 5. 验收标准

- 页眉只有**一个**通知铃铛（不再有站内信收件箱 + 告警铃铛两个图标）；角标显示统一未读数；下拉预览混合最近通知（消息 + 告警各带来源/级别）。
- 通知中心页（`/notifications`）可见消息 + 告警混合流；按类型筛选「消息/告警」生效；仅未读切换生效；关键字查询生效；分页可翻页；行内「标记已读」与「全部已读」生效，已读后角标减少。
- 侧栏「观测」域**不再有「告警」项**；「系统 · 账户与审计」出现「通知中心」入口，点击进 `/notifications`。
- 告警规则/通道/事件管理页 `/alerts` 仍可经通知中心「查看告警详情」或直链到达、功能不回归。
- breadcrumb：`/notifications` 显示「系统 › 通知中心」；`/alerts` 显示「系统 › 告警」。
- i18n zh/en 均有新键，切换语言文案正确。
- `go build ./... && go vet ./... && go test ./...` 全绿（含 feed 新单测）；`cd web && npx tsc --noEmit && npm run lint && npm run build && npx vitest run` 全绿。
- **真机验收（需用户确认，待真机验）**：真浏览器点页眉铃铛下拉、进通知中心筛选/标记已读、侧栏通知中心入口；造一条站内信 + 一条告警后均出现在统一流。单元/E2E 绿不替代此项。

## 6. Gate-API 自检

- [x] 所有 endpoint 已定义（路径/方法/请求 Query/响应体）——见 §3.1 表
- [x] 所有 error code 已定义（400/401/404）
- [x] 权限要求已标注（认证用户；message 按本人、alert 全局，与既有 `/alerts` 一致）
- [x] 与 ARCHITECTURE 通信协议一致（CP HTTP，聚合落 CP，Worker 不参与）
- [x] 与数据库模型一致（复用 notifications/alert_events，无新表）
- [x] 请求/响应 JSON 可直接生成 TS 类型（FeedItem/FeedQuery）
- [x] 每个 endpoint 标注关联 FR（FR-216）

## 7. 风险 / 待定

- **告警可见性非用户隔离**：告警面向全体运维（与既有 `/alerts` 一致），故通知流里告警部分对所有认证用户相同，仅站内信部分按本人。这与「通知中心=我的通知」的直觉略有出入，但保持与现状一致、不在本 FR 改告警可见性模型（如需按用户/组细分告警可见性，另起 FR）。
- **分页归并的 total 口径**：`total` 为两源命中数之和；跨源按时间归并切页，单页内消息/告警交错，符合「按时间倒序的统一流」。不做跨源去重（两源天然不重叠）。
- **与 FR-215 的 ConsoleSidebar 交接**：本 FR 基于 FR-215 已落 main 的侧栏结果再改（删过渡位 + 加入口），独占改 ConsoleSidebar 告警/页眉铃铛，与波 2a 其他 FR 不冲突。
- **旧站内信入口/键保留**：`NotificationInbox` 组件不再被页眉引用（可留作未引用组件或后续清理），既有 `/notifications/*` 端点与 i18n `notifications.*` 键保留，避免破坏其它引用。
