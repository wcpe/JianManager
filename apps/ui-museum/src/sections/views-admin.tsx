/**
 * @file 博物馆「业务视图 · 平台管理」分区：15 个受控复合视图（@jianmanager/ui · components/views）的登记样例。
 *
 * 这些视图都是受控复合组件：数据经 props 注入、动作以回调上报、路由/取数/toast 一律归外壳，
 * 故博物馆可以脱离主控台运行时把它们整页渲染出来。本分区里博物馆**扮演外壳**：
 * 少数「外壳持有的状态」（筛选键、当前分类、选中对象、统计窗口）在本文件里用 useState 代持，
 * 其余纯 UI 状态（对话框开合、草稿、展开行）仍留在各视图内部。
 *
 * 两点环境事实（不是组件缺陷）：
 * - 博物馆未初始化 i18next，`useTranslation` 取不到实例，视图内文案会以 i18n key 原样显示
 *   （如 `users.title`）；插值同样退化为 key。这是博物馆运行时的缺失，主控台不受影响。
 * - 模态对话框经 Radix Portal 渲染到 document.body 并铺全屏遮罩 + 焦点陷阱：常挂会把整个博物馆
 *   罩住（页面不可点、截图只剩弹窗）。故四个对话框视图沿用博物馆既有惯例（PromptDialog /
 *   DangerConfirm），由按钮触发挂载，而不是永久展开。
 */
import { useState, type ReactNode } from 'react'
import { Button, Panel, type MetricRange } from '@jianmanager/ui'
import type { AuditLogInfo } from '@jianmanager/ui/lib/audit-contracts'
import type { AuditFilterState } from '@jianmanager/ui/lib/audit-filters'
import type { ClientDistObservability } from '@jianmanager/ui/lib/client-dist-stats-contracts'
import type { NodeInfo } from '@jianmanager/ui/lib/node-types'
import type { OnlinePlayersResult } from '@jianmanager/ui/lib/player'
import type { SettingCategory } from '@jianmanager/ui/lib/settings-form'
import type { SettingsItemView } from '@jianmanager/ui/components/views/settings/SettingsPageView'

import { AuditPageView } from '@jianmanager/ui/components/views/audit/AuditPageView'
import { InvitePageView } from '@jianmanager/ui/components/views/auth/InvitePageView'
import { LoginPageView } from '@jianmanager/ui/components/views/auth/LoginPageView'
import { SetupPageView } from '@jianmanager/ui/components/views/auth/SetupPageView'
import { CreateGroupDialogView } from '@jianmanager/ui/components/views/groups/CreateGroupDialogView'
import { GroupEditDialogView } from '@jianmanager/ui/components/views/groups/GroupEditDialogView'
import {
  GroupMembersDialogView,
  type GroupMemberCandidate,
  type GroupMemberView,
} from '@jianmanager/ui/components/views/groups/GroupMembersDialogView'
import { LicensesPageView, type LicenseDepEntry } from '@jianmanager/ui/components/views/licenses/LicensesPageView'
import {
  NotificationCenterPageView,
  type NotificationFeedItem,
  type NotificationFeedPage,
  type NotificationFeedQuery,
} from '@jianmanager/ui/components/views/notifications/NotificationCenterPageView'
import {
  PermissionsPageView,
  type PermissionSelection,
  type PermissionsCatalogDomain,
  type PermissionsRoleRow,
  type PermissionsUserRow,
  type PermissionsUserPermissions,
} from '@jianmanager/ui/components/views/permissions/PermissionsPageView'
import { SettingsPageView } from '@jianmanager/ui/components/views/settings/SettingsPageView'
import { StatisticsPageView } from '@jianmanager/ui/components/views/statistics/StatisticsPageView'
import { UsersPageView, type UserInvitationRow, type UserRow } from '@jianmanager/ui/components/views/users/UsersPageView'
import { GroupsPageView, type GroupRow } from '@jianmanager/ui/components/views/users/GroupsPageView'
import { EditUserDialogView } from '@jianmanager/ui/components/views/EditUserDialogView'

/**
 * 页面级视图的预览框：给页面壳一个确定高度，让 `flex-1` + 内部 `overflow-auto` 真按「页面」滚动
 * （裸放进 Panel 时这些类都失效，长页会一路撑开博物馆）。高度取 720px ≈ 主控台内容区高度。
 */
function PageFrame({ children }: { children: ReactNode }) {
  return <div className="flex h-[720px] flex-col overflow-hidden rounded-md border">{children}</div>
}

/**
 * 整屏壳态视图（登录/初始化/邀请自带 `h-screen`）的裁剪框。
 * 它们按 100vh 上下居中，直接放进长滚动页面会留下一整屏空白，故裁剪预览。
 * 高度取 80vh（下界 560 / 上界 900）：卡片中心恒在 50vh 处，只要框高 ≥ 50vh + 半张卡（约 165px）
 * 就完整可见——0.8vh ≥ 0.5vh + 165px 在视口 ≥ 550px 时成立，与本下界一致。
 */
function ScreenFrame({ children }: { children: ReactNode }) {
  return <div className="h-[80vh] max-h-[900px] min-h-[560px] overflow-hidden rounded-md border">{children}</div>
}

/** 槽位占位（插槽实现属外壳）：说明「这里本应由外壳注入什么」而不假装已经渲染出来。 */
function SlotPlaceholder({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-md border border-dashed px-3 py-6 text-center text-[11px] text-muted-foreground">
      {children}
    </p>
  )
}

// --- 审计页样例：三条成功 + 一条失败留痕（FR-321） -------------------------------------------------

const auditLogs: AuditLogInfo[] = [
  {
    id: 9001,
    uuid: '8f0d3c4a-91b2-4e77-9c10-5a6f2b7d1e03',
    userId: 12,
    action: 'instance.start',
    targetType: 'instance',
    targetId: '42',
    detail: '{"name":"survival-01","nodeId":2}',
    ip: '10.0.0.11',
    failed: false,
    error: '',
    createdAt: '2026-07-05T09:12:33Z',
    user: { id: 12, username: 'ops_alpha' },
  },
  {
    id: 9002,
    uuid: '2c7b6a51-33d8-4f29-8a5e-1d9c4b0f7a66',
    userId: 12,
    action: 'settings.update',
    targetType: 'setting',
    targetId: 'log.level',
    detail: '{"key":"log.level","before":"info","after":"warn"}',
    ip: '10.0.0.11',
    failed: false,
    error: '',
    createdAt: '2026-07-05T09:05:12Z',
    user: { id: 12, username: 'ops_alpha' },
  },
  {
    id: 9003,
    uuid: 'b41a9e07-6c52-4d18-93f0-7e2a5c6b8d41',
    userId: 18,
    action: 'instance.launchspec.write',
    targetType: 'instance',
    targetId: '7',
    detail: '',
    ip: '10.0.0.24',
    failed: true,
    error: 'worker 返回 500：写入 env 失败（工作目录只读）',
    createdAt: '2026-07-05T08:58:40Z',
    user: { id: 18, username: 'build_robot' },
  },
  {
    id: 9004,
    uuid: 'e5d1f8a3-70b4-4c96-a2d7-3f6b1e9c0a25',
    userId: 9,
    action: 'permissions.user.update',
    targetType: 'user',
    targetId: '21',
    detail: '{"added":["terminal.access"],"removed":["instance.delete"]}',
    ip: '10.0.0.9',
    failed: false,
    error: '',
    createdAt: '2026-07-05T08:40:05Z',
    user: { id: 9, username: 'audit_bot' },
  },
]

/** 审计筛选的默认态（受控：真实外壳改一项即重新取数并回第 1 页）。 */
const EMPTY_AUDIT_FILTER: AuditFilterState = { userId: '', action: '', targetType: '', from: '', to: '' }

const auditUserOptions = [
  { id: 12, username: 'ops_alpha' },
  { id: 18, username: 'build_robot' },
  { id: 9, username: 'audit_bot' },
]

// --- 开源许可样例：运行时/开发两分区，含「无正文」「非链接」两条边界 -------------------------------

const MIT_TEXT = [
  'MIT License',
  '',
  'Permission is hereby granted, free of charge, to any person obtaining a copy',
  'of this software and associated documentation files (the "Software"), to deal',
  'in the Software without restriction, including without limitation the rights',
  'to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies',
  'of the Software…（正文略）',
].join('\n')

const licenseDeps: LicenseDepEntry[] = [
  {
    name: 'gin',
    version: 'v1.10.1',
    license: 'MIT',
    author: 'gin-gonic',
    url: 'https://github.com/gin-gonic/gin',
    scope: 'go',
    type: 'runtime',
    licenseText: MIT_TEXT,
  },
  {
    name: 'react',
    version: '19.2.6',
    license: 'MIT',
    author: 'Meta Platforms, Inc. and affiliates',
    url: 'https://react.dev',
    scope: 'web',
    type: 'runtime',
    licenseText: MIT_TEXT,
  },
  {
    name: 'mineflayer',
    version: '4.37.1',
    license: 'MIT',
    author: 'PrismarineJS',
    url: 'https://github.com/PrismarineJS/mineflayer',
    scope: 'bot-worker',
    type: 'runtime',
    licenseText: MIT_TEXT,
  },
  {
    name: 'typescript',
    version: '6.0.2',
    license: 'Apache-2.0',
    author: 'Microsoft Corporation',
    url: 'https://www.typescriptlang.org/',
    scope: 'web',
    type: 'dev',
    // 刻意留空：演示「许可证全文缺失」分支（行内展开显示无正文提示）。
    licenseText: '',
  },
  {
    name: 'shadow',
    version: '8.1.1',
    license: 'Apache-2.0',
    author: 'Gradle',
    // 刻意用非 https 值：演示「不可点链接」（只渲染纯文本包名）的分支。
    url: '',
    scope: 'client-updater',
    type: 'dev',
    licenseText: 'Apache License\n\nVersion 2.0, January 2004\nhttp://www.apache.org/licenses/\n\n（正文略）',
  },
]

// --- 通知中心样例：站内信 + 告警混排，含未读、任务联动与已读三类 --------------------------------

const feedItems: NotificationFeedItem[] = [
  {
    id: 301,
    source: 'alert',
    level: 'error',
    title: '实例 survival-01 连续 3 拍探针不可达',
    body: '假死判定阈值已触发，处置档为 warn（未自动重启）。',
    read: false,
    createdAt: '2026-07-05T09:20:00Z',
  },
  {
    id: 302,
    source: 'message',
    level: 'info',
    title: '二进制升级任务已完成',
    body: 'survival-01 已升级到 paper-1.20.4-496，升级前的二进制已记为回滚点。',
    read: false,
    createdAt: '2026-07-05T09:02:00Z',
    taskId: 'task-7f3a91',
  },
  {
    id: 303,
    source: 'alert',
    level: 'warning',
    title: '节点 node-bj-02 磁盘使用率 82%',
    body: '已超过告警阈值 80%，建议清理制品缓存或扩容。',
    read: true,
    createdAt: '2026-07-05T08:31:00Z',
  },
  {
    id: 304,
    source: 'message',
    level: 'success',
    title: '备份保留策略已裁剪 12 份超期备份',
    read: true,
    createdAt: '2026-07-05T07:15:00Z',
    taskId: 'task-1c02de',
  },
]

const feedPage: NotificationFeedPage = { items: feedItems, total: 6 }

// --- 权限页样例：目录取真实节点 id；角色/账号全为占位假名 ----------------------------------------

const permCatalog: PermissionsCatalogDomain[] = [
  {
    domain: 'platform',
    label: '平台',
    nodes: [
      { id: 'user.read', label: '用户读取' },
      { id: 'user.manage', label: '用户管理' },
      { id: 'group.manage', label: '用户组管理' },
      { id: 'settings.write', label: '设置写入' },
      { id: 'audit.read', label: '审计读取' },
      { id: 'rbac.manage', label: '权限配置管理' },
    ],
  },
  {
    domain: 'runtime',
    label: '运行时',
    nodes: [
      { id: 'instance.read', label: '实例读取' },
      { id: 'instance.operate', label: '实例启停' },
      { id: 'instance.delete', label: '删除实例' },
      { id: 'instance.launchspec.write', label: '启动规格 / 环境变量', risk: true },
      { id: 'instance.business.write', label: '业务高危写', risk: true },
      { id: 'file.write', label: '文件写入' },
      { id: 'terminal.access', label: '终端访问' },
    ],
  },
  {
    domain: 'observability',
    label: '观测',
    nodes: [
      { id: 'monitor.read', label: '监控' },
      { id: 'log.read', label: '日志' },
      { id: 'alert.read', label: '告警读取' },
      { id: 'alert.manage', label: '告警管理' },
    ],
  },
  {
    domain: 'workspace',
    label: '工作区',
    nodes: [
      { id: 'player.read', label: '玩家读取' },
      { id: 'player.manage', label: '玩家管理' },
      { id: 'network.read', label: '网络读取' },
    ],
  },
]

/** 目录里全部节点 id（平台管理员角色模板用：seed 口径就是全量）。 */
const ALL_PERM_NODES = permCatalog.flatMap((d) => d.nodes.map((n) => n.id))

/** 组管理员模板节点（下面用户覆盖样例的基线）。 */
const GROUP_ADMIN_NODES = [
  'user.read',
  'group.manage',
  'instance.read',
  'instance.operate',
  'instance.delete',
  'file.write',
  'monitor.read',
  'log.read',
  'alert.read',
  'alert.manage',
  'player.read',
  'player.manage',
]

const permRoles: PermissionsRoleRow[] = [
  {
    id: 1,
    key: 'platform_admin',
    name: '平台管理员',
    description: '内置超级管理员，权限树与覆盖均锁定',
    isSystem: true,
    nodes: ALL_PERM_NODES,
    updatedAt: '2026-06-01T00:00:00Z',
  },
  {
    id: 2,
    key: 'group_admin',
    name: '组管理员',
    description: '组内全量运维 + 成员管理',
    isSystem: true,
    nodes: GROUP_ADMIN_NODES,
    updatedAt: '2026-06-18T10:20:00Z',
  },
  {
    id: 3,
    key: 'group_operator',
    name: '组运维',
    description: '日常启停与排障，不可删除实例',
    isSystem: true,
    nodes: ['user.read', 'instance.read', 'instance.operate', 'file.write', 'monitor.read', 'log.read', 'player.read'],
    updatedAt: '2026-06-18T10:20:00Z',
  },
  {
    id: 4,
    key: 'group_viewer',
    name: '组只读',
    description: '仅查看实例与观测数据',
    isSystem: true,
    nodes: ['instance.read', 'monitor.read', 'log.read'],
    updatedAt: '2026-06-18T10:20:00Z',
  },
]

/** 假账号（不使用真实人名/真实身份）。 */
const permUsers: PermissionsUserRow[] = [
  { id: 101, username: 'ops_alpha', role: 2 },
  { id: 102, username: 'build_robot', role: 3 },
  { id: 103, username: 'plat_admin_demo', role: 10 },
]

/** 选中用户（ops_alpha）的有效权限：组管理员模板 ⊕ 两条覆盖（deny 移除删除权）。 */
const permUserPermissions: PermissionsUserPermissions = {
  roleId: 2,
  nodes: GROUP_ADMIN_NODES,
  overrides: [
    { node: 'instance.delete', effect: 'deny' },
    { node: 'terminal.access', effect: 'allow' },
  ],
}

// --- 设置页样例：键名与取值口径对齐后端 GET /settings；敏感项一律脱敏 ----------------------------

const settingsEditable: SettingsItemView[] = [
  // 日志（即时生效）
  { key: 'log.level', value: 'info', editable: true, sensitive: false, overridden: false, effectiveImmediately: true },
  { key: 'debug.mode', value: 'false', editable: true, sensitive: false, overridden: false, effectiveImmediately: true },
  // 运行时（经心跳/安装下发，非 CP 内即时生效）
  { key: 'graceful_stop.timeout', value: '30s', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'jdk.mirror.temurin', value: 'https://api.adoptium.net', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'runtime.mirror.nodejs', value: 'https://nodejs.org/dist', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'direct_probe.slp_timeout', value: '3s', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  // 运行策略（FR-299 / FR-326 / FR-460）：健康巡检与自愈、配额强制、崩溃统计保留、无主运行时与 Bot 回收
  // 取值一律对齐后端 defaultValue；probe_kind 空串表示「自动推断探针类型」，是真实的默认态。
  { key: 'health.scan_enabled', value: 'true', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'health.scan_interval', value: '30s', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'health.probe_kind', value: '', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'health.suspicion_threshold', value: '3', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'health.action', value: 'warn', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'health.circuit_breaker_threshold', value: '5', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'health.circuit_breaker_window', value: '10m', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'health.startup_warmup', value: '5m', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'health.self_heal_max_restarts', value: '3', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'quota.enforce_interval', value: '60s', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'quota.enforce_mode', value: 'alert', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'quota.enforce_streak', value: '5', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'crash.stat_retention_days', value: '90', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'instance_reverse_reconcile.grace_period', value: '10m', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'instance_reverse_reconcile.auto_dispose', value: 'false', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'bot_reclaim.grace_period', value: '2m', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'bot_reclaim.auto_reclaim', value: 'true', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  // 网络：proxy.url / github.token 都是敏感项
  // proxy.url 只回显 scheme://host:port（本例不含凭据），github.token 回显掩码而不回显明文。
  { key: 'proxy.url', value: 'http://proxy.example.internal:3128', editable: true, sensitive: true, overridden: false, effectiveImmediately: true },
  { key: 'proxy.no_proxy', value: 'localhost,127.0.0.1,.internal', editable: true, sensitive: false, overridden: false, effectiveImmediately: true },
  { key: 'github.token', value: '(已配置)', editable: true, sensitive: true, overridden: true, effectiveImmediately: true },
  // 备份：含整机快照的保留与占用上限（FR-3xx 快照族）
  { key: 'backup.retention_days', value: '30', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'snapshot.retention_count', value: '10', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'snapshot.retention_days', value: '30', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'snapshot.pre_rollback_keep', value: '3', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'snapshot.max_per_instance', value: '20', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  // max_total_mb=0 表示不设总量上限（后端默认），不是「上限为 0」。
  { key: 'snapshot.max_total_mb', value: '0', editable: true, sensitive: false, overridden: false, effectiveImmediately: false },
  // 邮件（邀请）：password 只回显「已配置」，输入框自身以 ${ENV_VAR} 作占位提示收引用串
  { key: 'platform.public_base_url', value: 'https://panel.example.com', editable: true, sensitive: false, overridden: true, effectiveImmediately: true },
  { key: 'invite.smtp.host', value: 'smtp.example.com', editable: true, sensitive: false, overridden: true, effectiveImmediately: true },
  { key: 'invite.smtp.port', value: '587', editable: true, sensitive: false, overridden: true, effectiveImmediately: true },
  { key: 'invite.smtp.username', value: 'no-reply@example.com', editable: true, sensitive: false, overridden: true, effectiveImmediately: true },
  { key: 'invite.smtp.password', value: '(已配置)', editable: true, sensitive: true, overridden: true, effectiveImmediately: true },
  { key: 'invite.smtp.from', value: 'JianManager <no-reply@example.com>', editable: true, sensitive: false, overridden: true, effectiveImmediately: true },
]

/** 只读项（security 分类，视觉隔离）：启动固定值 + 密钥类脱敏串。 */
const settingsReadOnly: SettingsItemView[] = [
  { key: 'server.host', value: '0.0.0.0', editable: false, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'server.port', value: '8080', editable: false, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'grpc.port', value: '9090', editable: false, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'database.driver', value: 'sqlite', editable: false, sensitive: false, overridden: false, effectiveImmediately: false },
  // 脱敏口径与后端一致：DSN 只打掉口令段。
  { key: 'database.dsn', value: 'jmuser:***@tcp(127.0.0.1:3306)/jianmanager', editable: false, sensitive: true, overridden: false, effectiveImmediately: false },
  // 密钥类只给占位掩码，不放任何可用的假凭据。
  { key: 'jwt.secret', value: '***', editable: false, sensitive: true, overridden: false, effectiveImmediately: false },
  { key: 'jwt.ws_secret', value: '(自动生成: 数据根 etc/ws-token-secret.key)', editable: false, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'jwt.access_ttl', value: '1h', editable: false, sensitive: false, overridden: false, effectiveImmediately: false },
  { key: 'jwt.refresh_ttl', value: '168h', editable: false, sensitive: false, overridden: false, effectiveImmediately: false },
]

/** 设置页分类导航顺序（与视图内 CATEGORY_ICON 的键一致）。 */
const SETTINGS_CATEGORIES: SettingCategory[] = [
  'appearance',
  'logging',
  'runtime',
  'policy',
  'network',
  'backup',
  'email',
  'security',
]

// --- 统计页样例：KPI / 节点 / 实例聚合 / 玩家 / 分发观测 ----------------------------------------

const statsNodes: NodeInfo[] = [
  {
    id: 1,
    uuid: '0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0',
    name: 'node-bj-01',
    host: '10.0.0.11',
    grpcPort: 9090,
    wsPort: 9091,
    status: 1,
    maintenance: false,
    tunnelConnected: true,
    os: 'linux',
    arch: 'amd64',
    cpuCores: 16,
    memoryMb: 65536,
    diskTotalMb: 1048576,
    cpuUsage: 38.5,
    memoryUsage: 51.2,
    diskUsage: 46.7,
    networkBytesSent: 9485760000,
    networkBytesRecv: 13201760000,
    loadAvg1: 2.4,
    lastHeartbeat: '2026-07-05T09:20:11Z',
    createdAt: '2026-03-01T00:00:00Z',
  },
  {
    id: 2,
    uuid: '1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f809',
    name: 'node-bj-02',
    host: '10.0.0.12',
    grpcPort: 9090,
    wsPort: 9091,
    status: 1,
    maintenance: true,
    tunnelConnected: true,
    os: 'linux',
    arch: 'arm64',
    cpuCores: 8,
    memoryMb: 32768,
    diskTotalMb: 524288,
    cpuUsage: 12.1,
    memoryUsage: 33.8,
    diskUsage: 82.4,
    networkBytesSent: 2147483648,
    networkBytesRecv: 4294967296,
    loadAvg1: 0.6,
    lastHeartbeat: '2026-07-05T09:20:09Z',
    createdAt: '2026-04-12T00:00:00Z',
  },
  {
    id: 3,
    uuid: '9c8b7a65-4321-0fed-cba9-876543210fed',
    name: 'node-sh-01',
    host: '10.0.0.11',
    grpcPort: 9090,
    wsPort: 9091,
    status: 0,
    maintenance: false,
    tunnelConnected: false,
    os: 'windows',
    arch: 'amd64',
    cpuCores: 12,
    memoryMb: 49152,
    diskTotalMb: 2097152,
    cpuUsage: 0,
    memoryUsage: 0,
    diskUsage: 61.3,
    networkBytesSent: 0,
    networkBytesRecv: 0,
    loadAvg1: 0,
    lastHeartbeat: null,
    createdAt: '2026-05-02T00:00:00Z',
  },
]

const statsPlayers: OnlinePlayersResult = {
  players: [
    { name: 'Steve_demo', instanceId: 42, instanceName: 'survival-01' },
    { name: 'Alex_demo', instanceId: 42, instanceName: 'survival-01' },
    { name: 'Builder_demo', instanceId: 51, instanceName: 'creative-01' },
  ],
  backends: [
    { instanceId: 42, instanceName: 'survival-01', available: true },
    { instanceId: 51, instanceName: 'creative-01', available: true },
    // 探针不可达：优雅降级提示（FR-067）
    { instanceId: 63, instanceName: 'skyblock-dev', available: false, error: '连接超时（3s）' },
  ],
}

const statsDistribution: ClientDistObservability = {
  channelId: 'stable',
  from: '2026-07-04T09:00:00Z',
  to: '2026-07-05T09:00:00Z',
  series: [
    {
      ts: '2026-07-05T07:00:00Z',
      manifestPulls: 120,
      artifactPulls: 34,
      downloadBytes: 2400000000,
      activeMachines: 42,
      updateTotal: 30,
      updateSuccess: 28,
      updateFailStatic: 1,
      updateRolledBack: 1,
      updateError: 0,
    },
    {
      ts: '2026-07-05T08:00:00Z',
      manifestPulls: 156,
      artifactPulls: 41,
      downloadBytes: 3200000000,
      activeMachines: 55,
      updateTotal: 34,
      updateSuccess: 32,
      updateFailStatic: 1,
      updateRolledBack: 1,
      updateError: 0,
    },
    {
      ts: '2026-07-05T09:00:00Z',
      manifestPulls: 88,
      artifactPulls: 22,
      downloadBytes: 1500000000,
      activeMachines: 31,
      updateTotal: 18,
      updateSuccess: 17,
      updateFailStatic: 0,
      updateRolledBack: 1,
      updateError: 0,
    },
  ],
  summary: {
    manifestPulls: 512,
    artifactPulls: 148,
    downloadBytes: 9876543210,
    updateTotal: 120,
    updateSuccess: 112,
    updateFailStatic: 4,
    updateRolledBack: 3,
    updateError: 1,
    successRate: 0.9333,
    failStaticRate: 0.0333,
    rollbackRate: 0.025,
    activeMachines: 96,
    activeMachinesExact: true,
  },
  // 分布项（FR-217）：版本 / 平台 / 滞后时长三张图的数据源。
  versionDist: [
    { version: 24, count: 61 },
    { version: 23, count: 12 },
    { version: 22, count: 4 },
  ],
  platformDist: [
    { os: 'windows', count: 52 },
    { os: 'linux', count: 21 },
    { os: 'darwin', count: 4 },
  ],
  lagDist: [
    { lag: 0, count: 55 },
    { lag: 1, count: 18 },
    { lag: 3, count: 2 },
  ],
}

// --- 用户 / 用户组样例：假账号；配额按后端「0 = 不限」口径 -------------------------------------

const userRows: UserRow[] = [
  { id: 9, username: 'audit_bot', role: 10, status: 0, createdAt: '2026-01-08T09:00:00Z' },
  { id: 12, username: 'ops_alpha', role: 2, status: 0, createdAt: '2026-02-11T03:20:00Z' },
  { id: 18, username: 'build_robot', role: 3, status: 0, createdAt: '2026-03-02T07:45:00Z' },
  { id: 21, username: 'temp_guest', role: 0, status: 1, createdAt: '2026-05-19T11:02:00Z' },
]

const userInvitations: UserInvitationRow[] = [
  { id: 501, email: 'newhire@example.com', expiresAt: '2026-07-20T00:00:00Z', revoked: false, used: false },
  { id: 502, email: 'dba@example.com', expiresAt: '2026-07-19T00:00:00Z', revoked: true, used: false },
  { id: 503, email: 'intern@example.com', expiresAt: '2026-06-30T00:00:00Z', revoked: false, used: true },
]

const groupRows: GroupRow[] = [
  {
    id: 11,
    name: '生存服运营组',
    description: '负责 survival 系列后端的日常启停、配置与玩家处置',
    quota: { maxInstances: 20, maxBots: 200, maxStorageMb: 51200 },
    members: [
      { id: 1, userId: 12, role: 1, user: { username: 'ops_alpha' } },
      { id: 2, userId: 18, role: 0, user: { username: 'build_robot' } },
    ],
  },
  {
    id: 12,
    name: '压测组',
    description: 'Bot 舰队压测与容量验证，配额按窗口临时上调',
    // 0 = 不限（与后端口径一致）：Bot 与存储不限，仅限实例数。
    quota: { maxInstances: 6, maxBots: 0, maxStorageMb: 0 },
    members: [{ id: 3, userId: 21, role: 0, user: { username: 'temp_guest' } }],
  },
  {
    id: 13,
    name: '只读观察组',
    description: '审计与客服只读账号',
    members: [],
  },
]

/** 成员管理对话框的样例：现有成员 + 服务端候选（候选总数 > 已显示数，触发截断提示）。 */
const groupMembers: GroupMemberView[] = [
  { id: 1, userId: 12, role: 1, user: { username: 'ops_alpha' } },
  { id: 2, userId: 18, role: 0, user: { username: 'build_robot' } },
]

const groupMemberCandidates: GroupMemberCandidate[] = [
  { id: 21, username: 'temp_guest' },
  { id: 9, username: 'audit_bot' },
  { id: 31, username: 'qa_demo' },
]

/** 博物馆「业务视图 · 平台管理」分区：受控复合组件，数据经 props 注入，组件不取数、不碰路由。 */
export function ViewsAdmin() {
  // 外壳持有的受控状态（见文件头注释）：筛选键 / 选中对象 / 当前分类 / 统计窗口。
  const [auditFilter, setAuditFilter] = useState<AuditFilterState>({ ...EMPTY_AUDIT_FILTER, action: 'instance.' })
  const [permSelection, setPermSelection] = useState<PermissionSelection>({ kind: 'role', id: 2 })
  const [settingsCategory, setSettingsCategory] = useState<SettingCategory>('email')
  const [statsRange, setStatsRange] = useState<MetricRange>('24h')
  const [feedQuery, setFeedQuery] = useState<NotificationFeedQuery>({ page: 1, pageSize: 50 })

  // 对话框视图的挂载开关（模态不能常开，见文件头注释）。
  const [createGroupOpen, setCreateGroupOpen] = useState(false)
  const [groupEditOpen, setGroupEditOpen] = useState(false)
  const [groupMembersOpen, setGroupMembersOpen] = useState(false)
  const [editUserOpen, setEditUserOpen] = useState(false)

  return (
    <>
      <Panel title="AuditPageView · 审计日志检索">
        <PageFrame>
          <AuditPageView
            logs={auditLogs}
            total={128}
            users={auditUserOptions}
            filter={auditFilter}
            hasNextPage
            onChangeFilter={(patch) => setAuditFilter((prev) => ({ ...prev, ...patch }))}
            onResetFilter={() => setAuditFilter({ ...EMPTY_AUDIT_FILTER })}
            onLoadMore={() => {}}
            onExport={() => {}}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a 范式）：行数据与命中总数由外壳取数注入；筛选五维是查询键，整份由外壳持有
          （博物馆代持，故键入即改控件态），改一项后重新取数并回第 1 页、导出 NDJSON、加载下一页
          都由外壳执行；展开行的目标与实测高度留在组件内——它只切换本地展示、不产生请求。
        </p>
      </Panel>

      <Panel title="InvitePageView · 接受邀请（公开页）">
        <ScreenFrame>
          <InvitePageView
            onSubmit={async () => {}}
            renderLoginLink={({ to, children }) => (
              <a href={to}>{children}</a>
            )}
          />
        </ScreenFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：接受邀请经 onSubmit 上报（失败请抛错，视图取服务端 message 作内联错误）；令牌只在容器侧从
          URL fragment 读取，视图不接触，接受成功后清理 URL 也由容器负责。renderLoginLink 插槽用于把
          「前往登录」换成路由 Link（缺省就是原生 &lt;a&gt;，本例即注入该缺省形态），跳转动作由外壳执行。
          整屏壳态（min-h-screen）在博物馆里以 80vh 裁剪框预览（上界 900px）。
        </p>
      </Panel>

      <Panel title="LoginPageView · 登录">
        <ScreenFrame>
          <LoginPageView onSubmit={async () => {}} />
        </ScreenFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：表单草稿与内联错误留在视图内；登录请求、token 落地与 returnTo 跳转都在外壳（提交成功视图不导航）。
          submitting 由外壳按 mutation 在途注入（在途时视图自己短路重复提交），loading 为「初始化状态查询中」的居中加载态，
          是否该重定向到初始化页由外壳先行判定。整屏壳态（h-screen）在博物馆里以 80vh 裁剪框预览（上界 900px）。
        </p>
      </Panel>

      <Panel title="SetupPageView · 首次初始化">
        <ScreenFrame>
          <SetupPageView onSubmit={async () => {}} />
        </ScreenFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：密码强度条、两次输入一致性提示与校验错误是视图内的纯表单态；初始管理员的创建请求、token 落地与
          登录后跳转归外壳（submitting / loading 皆由外壳注入）。整屏壳态（h-screen）在博物馆里以 80vh 裁剪框预览（上界 900px）。
        </p>
      </Panel>

      <Panel title="CreateGroupDialogView · 新建用户组">
        <Button variant="outline" size="sm" className="w-fit" onClick={() => setCreateGroupOpen(true)}>
          打开 CreateGroupDialogView
        </Button>
        <CreateGroupDialogView
          open={createGroupOpen}
          onClose={() => setCreateGroupOpen(false)}
          submitting={false}
          onSubmit={async () => {}}
        />
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：开关经 open/onClose 注入、创建经 onSubmit 上报（失败请抛错，视图回显服务端 message）、待提交态由
          submitting 注入；表单草稿与错误展示时机门控留在视图内。模态经 Portal 铺全屏遮罩，常挂会罩住整页，
          故此处按按钮挂载；创建请求由外壳执行。
        </p>
      </Panel>

      <Panel title="GroupEditDialogView · 编辑用户组（名称/描述 + 配额）">
        <Button variant="outline" size="sm" className="w-fit" onClick={() => setGroupEditOpen(true)}>
          打开 GroupEditDialogView
        </Button>
        {groupEditOpen && (
          <GroupEditDialogView
            group={{
              name: '生存服运营组',
              description: '负责 survival 系列后端的日常启停、配置与玩家处置',
              quota: { maxInstances: 20, maxBots: 200, maxStorageMb: 51200 },
            }}
            submitting={false}
            onClose={() => setGroupEditOpen(false)}
            onSubmit={async () => {}}
          />
        )}
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：初始值经 group 注入（配额缺省按「0 = 不限」呈现），提交经 onSubmit 上报，关闭经 onClose 上报；
          表单态、必填校验与内联错误留在视图内，请求、toast 与缓存失效归外壳。视图自身没有 open 属性
          （挂载即打开），故由本分区条件挂载；调用约定是父级以组 id 作 key 渲染，切换组时重置表单。
        </p>
      </Panel>

      <Panel title="GroupMembersDialogView · 组成员管理">
        <Button variant="outline" size="sm" className="w-fit" onClick={() => setGroupMembersOpen(true)}>
          打开 GroupMembersDialogView
        </Button>
        {groupMembersOpen && (
          <GroupMembersDialogView
            groupName="生存服运营组"
            members={groupMembers}
            candidates={groupMemberCandidates}
            candidateTotal={12}
            adding={false}
            removing={false}
            onQueryChange={() => {}}
            onAdd={() => {}}
            onRemove={() => {}}
            onClose={() => setGroupMembersOpen(false)}
          />
        )}
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：成员列表与候选窗口经 props 注入，键入经 onQueryChange 上报（300ms 防抖与候选请求是外壳策略，
          故本分区不响应键入——候选为静态注入），加入/移除经 onAdd/onRemove 上报。candidateTotal 大于已注入候选数，
          正是「已显示前 N / 共 total」截断提示的触发条件；仅用于演示该分支。挂载即打开，故按按钮挂载。
        </p>
      </Panel>

      <Panel title="LicensesPageView · 开源许可与依赖清单">
        <PageFrame>
          <LicensesPageView
            dependencies={licenseDeps}
            generatedAt="2026-07-01T08:00:00Z"
            onBack={() => {}}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：清单与加载/错误态经 props 注入（外壳读静态 /licenses.json）；页头「返回」以回调上报，
          由外壳接 navigate(-1)。包名搜索与行内展开是纯 UI 状态，留视图内（本地过滤已取回的清单，不触发取数）。
          样例含两条边界：typescript 的 licenseText 为空（演示「无正文」分支）、shadow 的 url 非 https（演示不可点链接分支）。
        </p>
      </Panel>

      <Panel title="NotificationCenterPageView · 统一通知中心">
        <PageFrame>
          <NotificationCenterPageView
            query={feedQuery}
            data={feedPage}
            markingAll={false}
            onPatchFilter={(patch) => setFeedQuery((prev) => ({ ...prev, page: 1, ...patch }))}
            onPageChange={(page) => setFeedQuery((prev) => ({ ...prev, page }))}
            onMarkAllRead={() => {}}
            onMarkRead={() => {}}
            onOpenTask={() => {}}
            onOpenAlertDetail={() => {}}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：通知流经 props 注入；来源/仅未读/关键字/页码整份由外壳持有（博物馆代持，
          并在补丁里代做「改筛选即回第 1 页」），视图只用 query 渲染选中态、不存筛选副本。
          标记单条/全部已读与「查看任务」「查看告警详情」两处跳转都以回调上报，由外壳执行 mutation 与 navigate；
          原始页无独立加载/错误态，未取回时以空列表渲染空态，此处保持一致（样例 total=6 而只注入 4 条，故分页器可见）。
        </p>
      </Panel>

      <Panel title="PermissionsPageView · 权限配置（三栏工作区）">
        <PageFrame>
          <PermissionsPageView
            catalog={permCatalog}
            roles={permRoles}
            users={permUsers}
            selection={permSelection}
            onSelectionChange={setPermSelection}
            userPermissions={permUserPermissions}
            canRead
            canManage
            saving={false}
            onSaveRolePermissions={async () => true}
            onSaveUserPermissions={async () => true}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：目录/角色/用户/选中用户的有效权限经 props 注入；选中对象是取数查询键且要读写 ?user=&lt;id&gt; 深链，
          故由外壳持有——本分区代持以便点击左栏切换对象。搜索词、Shift 连选范围、未保存草稿与保存确认弹窗留在视图内；
          鉴权由外壳注入（canRead/canManage），平台管理员角色与用户的锁定由视图按目录数据叠加。保存是显式保存：
          两个回调（角色模板 / 用户覆盖）返回是否成功，视图据此决定是否关确认窗。目录与角色/账号全为占位假名。
        </p>
      </Panel>

      <Panel title="SettingsPageView · 系统设置（分类 + 平台配置）">
        <div className="mb-2 flex flex-wrap gap-1">
          {SETTINGS_CATEGORIES.map((category) => (
            <Button
              key={category}
              size="xs"
              variant={settingsCategory === category ? 'default' : 'outline'}
              onClick={() => setSettingsCategory(category)}
            >
              {category}
            </Button>
          ))}
        </div>
        <PageFrame>
          <SettingsPageView
            isPlatformAdmin
            category={settingsCategory}
            onCategoryChange={setSettingsCategory}
            data={{ editable: settingsEditable, readOnly: settingsReadOnly }}
            saving={false}
            onSave={async () => true}
            theme="system"
            onThemeChange={() => {}}
            language="zh"
            onLanguageChange={() => {}}
            renderOutboundTest={({ category }) => (
              <SlotPlaceholder>
                出站连通性测试按钮（插槽 · 外壳注入）· 当前分类：{category}
              </SlotPlaceholder>
            )}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：配置数据与保存经 props 往来——onSave 返回是否成功，成功才清掉已落库的草稿键；
          当前分类是筛选语义，归外壳（本分区代持，上面的按钮即外壳行为）；明暗与语言的值/变更也是 props（外壳接主题 store 与 i18n）。
          留组件内的是各键草稿、切分类未保存拦截弹窗与分类导航的未保存指示。
          分类口径：keyCategory 把「后端允许编辑」的键归入可编辑分区，只读项落 security（该分区只渲染只读行）。
          这里刻意把 health.* / quota.* / snapshot.* / instance_reverse_reconcile.* / bot_reclaim.* / crash.* 与
          runtime.mirror.nodejs 都摆进来：它们曾因未登记前缀族而兜底落进 security，在界面上完全不可见，
          在组件受控化迁包时才被发现——现在由 packages/ui 的 settings-form.test.ts 守卫用例盯着（解析后端
          settings.go 的可编辑键清单，任何一个掉进 security 就变红）。
          <strong className="font-medium text-foreground">敏感项：</strong>
          样例里 invite.smtp.password / github.token / proxy.url 都是脱敏回显（「(已配置)」或仅 scheme://host:port，
          jwt.secret 为 ***），不含任何真实凭据——值是否回显、以何种形态回显由外壳与后端决定；
          输入框自身以 {'${ENV_VAR}'} 作占位提示收引用串。出站测试按钮自带取数与 mutation，故走 renderOutboundTest 插槽注入。
        </p>
      </Panel>

      <Panel title="StatisticsPageView · 观测统计">
        <PageFrame>
          <StatisticsPageView
            range={statsRange}
            onRangeChange={setStatsRange}
            overviewTotals={{ nodeCount: 3, onlineNodeCount: 2, runningInstances: 18, onlinePlayers: 47 }}
            nodes={statsNodes}
            instanceCounts={{
              total: 26,
              byStatus: { RUNNING: 18, STOPPED: 6, CRASHED: 2 },
              byRole: { backend: 20, proxy: 4, universal: 2 },
              byProcessType: { direct: 22, docker: 4 },
            }}
            players={statsPlayers}
            isPlatformAdmin
            distribution={statsDistribution}
            renderSloSection={() => (
              <SlotPlaceholder>可用性区块（FR-463 · 插槽）：取数在应用侧接线层，博物馆不注入</SlotPlaceholder>
            )}
            renderPlayerTrend={() => (
              <SlotPlaceholder>玩家在线趋势区块（FR-469 · 插槽）：取数在应用侧接线层，博物馆不注入</SlotPlaceholder>
            )}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（a 范式）：五个查询结果经 props 注入（overview / nodes / instanceCounts / players / distribution），
          分桶、占比与探针可达汇总都是纯展示派生、留视图内；统计窗口 range 是查询键（本分区代持，页头 RangePicker 即外壳行为），
          分发的区间枚举换算与平台管理员门禁也归外壳——非管理员必须不发起分发观测请求，故该判定不能留在视图。
          SLO 与玩家趋势两块自带应用侧取数与接线层，故经插槽注入（此处仅占位，动作由外壳执行）。
        </p>
      </Panel>

      <Panel title="UsersPageView · 用户管理">
        <PageFrame>
          <UsersPageView
            users={userRows}
            invitations={userInvitations}
            canManageUsers
            dangerAllowed
            updating={false}
            revoking={false}
            notify={() => {}}
            onCreateUser={async () => {}}
            onCreateInvitation={async () => ({ invitationUrl: 'https://panel.example.com/invite#token=demo' })}
            onToggleStatus={() => {}}
            onUpdateUser={async () => {}}
            onDeleteUser={() => {}}
            onRevokeInvitation={() => {}}
            renderPermissionsLink={({ to, title, children }) => (
              <a href={to} title={title}>
                {children}
              </a>
            )}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：用户列表、邀请列表、在途态与鉴权（canManageUsers / dangerAllowed）全由外壳注入；
          五个写动作以回调上报（新建用户、签发邀请、更新用户返回 Promise，失败请抛错以便对话框回显服务端 message；
          删除与撤销邀请为即发即弃），成功文案由外壳弹（notify 是空通道，组件库不弹 toast）。
          三个对话框开合、待删除确认目标与卡片/列表视图切换是纯 UI 状态，留组件内。
          renderPermissionsLink 是跳转插槽：包内只拼 /permissions?user=&lt;id&gt;，路由跳转由外壳执行（本例注入原生锚点）。
          列表里的账号与权限全为占位假名。
        </p>
      </Panel>

      <Panel title="GroupsPageView · 用户组管理">
        <PageFrame>
          <GroupsPageView
            groups={groupRows}
            activeGroupId={11}
            activePanel={null}
            onOpenPanel={() => {}}
            onClosePanel={() => {}}
            dangerAllowed
            creating={false}
            updating={false}
            onCreateGroup={async () => {}}
            onUpdateGroup={async () => {}}
            onDeleteGroup={() => {}}
            renderMembersDialog={({ groupId }) => (
              <SlotPlaceholder>
                成员管理对话框（插槽 · 外壳注入）· 目标组 #{groupId} —— 候选默认窗口、键入防抖与服务端搜索是外壳策略
              </SlotPlaceholder>
            )}
          />
        </PageFrame>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：组列表与鉴权（dangerAllowed）由外壳注入；深链选中组与面板（?group=&lt;id&gt;&amp;panel=edit|members）
          随路由变化、前进后退要复原，故解析与写回都在外壳（博物馆不读路由，故以 activeGroupId=11 / panel=null 的静态口径展示，
          点「编辑/管理成员」不会开面板）。创建/编辑/删除三个写动作以回调上报，其中两个返回 Promise 供对话框回显服务端 message；
          创建弹窗开合与待删除确认目标留组件内。renderMembersDialog 是插槽：视图只决定「哪个组、何时打开」，实现由外壳注入。
        </p>
      </Panel>

      <Panel title="EditUserDialogView · 编辑用户（角色 + 可选改密）">
        <Button variant="outline" size="sm" className="w-fit" onClick={() => setEditUserOpen(true)}>
          打开 EditUserDialogView
        </Button>
        {editUserOpen && (
          <EditUserDialogView
            user={{ username: 'ops_alpha', role: 2 }}
            submitting={false}
            onClose={() => setEditUserOpen(false)}
            onSubmit={async () => {}}
          />
        )}
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：初始值经 user 注入（父级以 user.id 作 key 渲染，切换用户时表单重置），更新经 onSubmit 上报
          （只上报真正改动的字段；无改动时视图直接关闭、不发请求），待提交态由 submitting 注入；
          角色下拉与密码下限校验留在视图内。挂载即打开，故按按钮挂载；更新请求由外壳执行。
        </p>
      </Panel>
    </>
  )
}
