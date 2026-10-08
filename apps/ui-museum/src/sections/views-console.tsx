/**
 * @file 博物馆「业务视图 · 控制台与实例」分区（sections/views-console）：把 components/views 下九个
 *       控制台 / 实例 / 数据库 / 资源管理器类业务视图按统一形态集中登记——每个视图一个 <Panel>，
 *       注入静态样例数据，动作回调一律空实现或恒真 Promise，插槽给最简实现。
 *
 * 这些视图都是受控复合组件（ADR-097 a/b 范式）：**不取数、不发请求、不弹 toast、不碰路由**，
 * 数据经 props 注入、动作经回调上报——真实主控台里「取数 + 路由 + 提示 + 权限门禁」全在外壳。
 * 本例的所有回调都是空壳（`() => {}` / `() => Promise.resolve(...)`），故点击不会有任何后果。
 *
 * 环境说明：博物馆没有 react-router / react-query / sonner / zustand，且未初始化 i18n 资源，
 * 因此①路由链接类插槽改用原生 <a>（`renderLink`）；②视图内的文案取自 i18n 上下文，未挂资源时
 * react-i18next 会回退为键名（如 `instances.start`），与本页既有分区表现一致。
 */
import { useState } from 'react'

import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Panel,
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui'
import type { BotInfo, BotSummaryGroup } from '@jianmanager/ui/lib/bot'
import type { ConfigDiff, ConfigVersion } from '@jianmanager/ui/lib/config-contracts'
import type { DbRowsResult, DbTableInfo } from '@jianmanager/ui/lib/db-contracts'
import type { FileInfo } from '@jianmanager/ui/lib/file-entry'
import type { ResourceSegment, TabKey } from '@jianmanager/ui/lib/instance-console-tabs'
import type { InstanceMetricsData } from '@jianmanager/ui/lib/instance-metrics'
import type { InstanceInfo } from '@jianmanager/ui/lib/instance-types'

import { BotGroupActions, BotGroupPeek, BotGroupRow } from '@jianmanager/ui/components/views/bots/BotGroupPartsView'
import ConfigVersionDrawer from '@jianmanager/ui/components/views/config-explorer/ConfigVersionDrawer'
import { InstanceConsolePageView } from '@jianmanager/ui/components/views/console/InstanceConsolePageView'
import InstanceResourceSegmentView from '@jianmanager/ui/components/views/console/InstanceResourceSegmentView'
import { DatabaseExplorerView } from '@jianmanager/ui/components/views/database/DatabaseExplorerView'
import { DatabaseRowsView, type DatabaseRowsState } from '@jianmanager/ui/components/views/database/DatabaseRowsView'
import FileTree from '@jianmanager/ui/components/views/explorer/FileTree'
import CloneInstanceDialogView from '@jianmanager/ui/components/views/instances/CloneInstanceDialogView'
import { InstanceRowView } from '@jianmanager/ui/components/views/instances/InstanceRowView'

/* ------------------------------------------------------------------ *
 * 样例数据：形状与真实接口响应一致（tags 用后端原样返回的 JSON 串等）
 * ------------------------------------------------------------------ */

/** 实例行样例 · 代理：运行中、已展开行内联后端、无漂移。 */
const DEMO_PROXY_INSTANCE: InstanceInfo = {
  id: 1,
  uuid: '3f9c1a72-5b4e-4d18-9c02-7a6f0e5d1b88',
  nodeId: 1,
  name: 'survival-proxy',
  type: 'minecraft_java',
  role: 'proxy',
  processType: 'daemon',
  status: 'RUNNING',
  startCommand: 'java -Xms512M -Xmx2G -jar velocity.jar',
  workDir: '/srv/jm/instances/survival-proxy',
  serverPort: 25565,
  autoStart: true,
  autoRestart: true,
  // 后端以 JSON 字符串原样返回标签，消费前统一经 parseTags() 规范化。
  tags: '["env:prod","entry"]',
  createdAt: '2026-05-02T09:31:00Z',
}

/** 实例行样例 · 后端子服：已停止 + 勾选 + 运行态漂移（工作目录下有未纳管活进程），可看漂移标记。 */
const DEMO_BACKEND_INSTANCE: InstanceInfo = {
  id: 2,
  uuid: 'a17b24d0-9e38-4c55-b1f7-2d84c6ee5a10',
  nodeId: 2,
  name: 'survival-overworld',
  type: 'minecraft_java',
  role: 'backend',
  processType: 'daemon',
  status: 'STOPPED',
  startCommand: 'java -Xms4G -Xmx4G -jar paper.jar nogui',
  workDir: '/srv/jm/instances/survival-overworld',
  serverPort: 25566,
  autoStart: false,
  autoRestart: true,
  tags: '["env:prod","region:r1"]',
  runtimeDriftPid: 20481,
  runtimeDriftCmdline: 'java -Xmx4G -jar paper.jar nogui',
  createdAt: '2026-05-06T14:02:00Z',
}

/** Bot 分组样例：一组规模大（窥视需翻页）、一组小（窥视只有一页）。 */
const DEMO_BOT_GROUPS: BotSummaryGroup[] = [
  { key: 'env:prod', label: '生产环境 · env:prod', total: 23, online: 18 },
  { key: 'env:dev', label: '开发环境 · env:dev', total: 4, online: 1 },
]

/** 窥视成员样例（第 2 页，页大小固定 10——BOT_PEEK_PAGE_SIZE）。 */
const DEMO_PEEK_BOTS: BotInfo[] = [
  {
    id: 11,
    uuid: 'b0c41f6e-3a2d-4f90-8c71-5e2b7d9a4413',
    instanceId: 1,
    instanceName: 'survival-proxy',
    name: 'guard-11',
    status: 'working',
    config: '{"server":"127.0.0.1","port":25566,"auth":"offline"}',
    behavior: 'guard',
    workerId: 'node-01',
    createdAt: '2026-07-01T08:00:00Z',
    updatedAt: '2026-07-05T10:20:00Z',
  },
  {
    id: 12,
    uuid: 'c5d2e8a1-77b4-4a3e-9012-6fb1c8d0e507',
    instanceId: 1,
    instanceName: 'survival-proxy',
    name: 'guard-12',
    status: 'idle',
    config: '{"server":"127.0.0.1","port":25566,"auth":"offline"}',
    behavior: 'idle',
    workerId: 'node-01',
    createdAt: '2026-07-01T08:00:00Z',
    updatedAt: '2026-07-05T10:18:00Z',
  },
  {
    id: 13,
    uuid: 'd8e6f1b2-04c7-4de5-a3f8-19b2c7d6e840',
    instanceId: 2,
    instanceName: 'survival-overworld',
    name: 'patrol-13',
    status: 'error',
    lastError: 'bot 依赖未安装（mineflayer 缺失）',
    config: '{"server":"127.0.0.1","port":25566,"auth":"offline"}',
    behavior: 'patrol',
    workerId: 'node-02',
    createdAt: '2026-07-02T11:30:00Z',
    updatedAt: '2026-07-05T09:55:00Z',
  },
]

/** 文件树样例：`fetchEntries` 的静态替身——按目录路径返回条目（FileTree 只保留 isDir 项）。 */
const DEMO_FILE_TREE: Record<string, FileInfo[]> = {
  '': [
    { name: 'plugins', isDir: true, size: 0, modTime: 1783000000 },
    { name: 'world', isDir: true, size: 0, modTime: 1783000000 },
    { name: 'logs', isDir: true, size: 0, modTime: 1783000000 },
    { name: 'server.properties', isDir: false, size: 1423, modTime: 1783001100, modeString: 'rw-r--r--' },
  ],
  plugins: [
    { name: 'EssentialsX', isDir: true, size: 0, modTime: 1782900000 },
    { name: 'EssentialsX.jar', isDir: false, size: 4_100_000, modTime: 1782900000, modeString: 'rw-r--r--' },
    { name: 'LuckPerms.jar', isDir: false, size: 2_800_000, modTime: 1782850000, modeString: 'rw-r--r--' },
  ],
  world: [
    { name: 'region', isDir: true, size: 0, modTime: 1783050000 },
    { name: 'level.dat', isDir: false, size: 2884, modTime: 1783050000, modeString: 'rw-r--r--' },
  ],
}

/** 文件树取数替身（外壳注入的 api 调用）：懒加载时按路径查表，未知路径给空数组。 */
async function demoFetchEntries(_instanceId: number, path: string): Promise<FileInfo[]> {
  return DEMO_FILE_TREE[path] ?? []
}

/** 数据库表清单样例：`rowCount` 为 -1 表示未统计（视图显 `?`）。 */
const DEMO_DB_TABLES: DbTableInfo[] = [
  { name: 'instances', rowCount: 42 },
  { name: 'users', rowCount: 7 },
  { name: 'audit_logs', rowCount: -1 },
]

/** 数据库行样例：含 JSON 对象单元格（走 JSON 分支）、null 单元格（空占位）与敏感列（前端兜底打码）。 */
const DEMO_DB_ROWS: Record<string, DbRowsResult> = {
  instances: {
    table: 'instances',
    columns: [
      { name: 'id', type: 'INTEGER', sensitive: false },
      { name: 'name', type: 'VARCHAR', sensitive: false },
      { name: 'status', type: 'VARCHAR', sensitive: false },
      { name: 'tags', type: 'JSON', sensitive: false },
    ],
    rows: [
      { id: 1, name: 'survival-proxy', status: 'RUNNING', tags: ['env:prod', 'entry'] },
      { id: 2, name: 'survival-overworld', status: 'STOPPED', tags: ['env:prod'] },
      { id: 3, name: 'creative-plot', status: 'CRASHED', tags: null },
    ],
    page: 1,
    pageSize: 25,
    total: 42,
  },
  users: {
    table: 'users',
    columns: [
      { name: 'id', type: 'INTEGER', sensitive: false },
      { name: 'username', type: 'VARCHAR', sensitive: false },
      { name: 'role', type: 'VARCHAR', sensitive: false },
      { name: 'password_hash', type: 'VARCHAR', sensitive: true },
    ],
    rows: [
      { id: 1, username: 'steve', role: 'admin', password_hash: '演示用假值（不涉真实凭据）' },
      { id: 2, username: 'alex', role: 'operator', password_hash: '演示用假值（不涉真实凭据）' },
    ],
    page: 1,
    pageSize: 25,
    total: 2,
  },
  audit_logs: {
    table: 'audit_logs',
    columns: [
      { name: 'id', type: 'INTEGER', sensitive: false },
      { name: 'action', type: 'VARCHAR', sensitive: false },
      { name: 'created_at', type: 'DATETIME', sensitive: false },
    ],
    rows: [],
    page: 1,
    pageSize: 25,
    total: 0,
  },
}

/** 配置版本列表样例：含一条「回滚自 #8」记录，可看版本行尾的 `← #8` 标注。 */
const DEMO_CONFIG_VERSIONS: ConfigVersion[] = [
  {
    id: 12,
    filePath: 'server.properties',
    message: '把 max-players 提到 40（配合暑期活动）',
    authorId: 3,
    createdAt: '2026-07-05T10:12:00Z',
  },
  {
    id: 11,
    filePath: 'server.properties',
    message: '开启 online-mode',
    authorId: 2,
    createdAt: '2026-07-04T21:40:00Z',
  },
  {
    id: 10,
    filePath: 'server.properties',
    message: '', // 空说明走「无说明」占位
    authorId: 1,
    createdAt: '2026-07-03T08:05:00Z',
    rollbackOfVersionId: 8,
  },
]

/** 配置 diff 样例（#11 → #12）。 */
const DEMO_CONFIG_DIFF: ConfigDiff = {
  fromVersionId: 11,
  toVersionId: 12,
  unifiedDiff:
    '--- a/server.properties\n+++ b/server.properties\n@@ -1,5 +1,5 @@\n motd=A Minecraft Server\n-max-players=20\n+max-players=40\n online-mode=true\n difficulty=hard',
  fromContent: 'motd=A Minecraft Server\nmax-players=20\nonline-mode=true\ndifficulty=hard',
  toContent: 'motd=A Minecraft Server\nmax-players=40\nonline-mode=true\ndifficulty=hard',
}

/** 单表行浏览的查询状态样例（真实场景由外壳持有：任一项变化都要重新请求，故不上提给视图）。 */
const DEMO_DB_QUERY: DatabaseRowsState = {
  page: 1,
  pageSize: 25,
  sort: 'id',
  order: 'asc',
  filterColumn: '',
  filterValue: '',
}

/** 带过滤条件的查询状态样例（过滤列 + 关键字已生效，可看过滤条上的「清除」按钮）。 */
const DEMO_DB_QUERY_FILTERED: DatabaseRowsState = {
  page: 1,
  pageSize: 25,
  sort: 'username',
  order: 'asc',
  filterColumn: 'username',
  filterValue: 'steve',
}

/** 控制台页样例实例（运行中，具备 MC 世界语义）。 */
const DEMO_CONSOLE_INSTANCE: InstanceInfo = {
  ...DEMO_BACKEND_INSTANCE,
  status: 'RUNNING',
  runtimeDriftPid: 0,
  runtimeDriftCmdline: '',
}

/** 控制台页样例指标一拍：探针可用，故 TPS/MSPT 走真实值分支。 */
const DEMO_CONSOLE_METRICS: InstanceMetricsData = {
  tps: 19.6,
  onlinePlayers: 37,
  memoryMb: 5200,
  msptMillis: 12,
  threads: 214,
  cpuPercent: 63,
  heapMaxMb: 8192,
  uptimeSeconds: 98_320,
  worlds: null,
  probeAvailable: true,
  playersAvailable: true,
  motd: 'A Minecraft Server',
  motdAvailable: true,
  version: '1.20.4',
  versionAvailable: true,
  favicon: '',
  maxPlayers: 60,
  maxPlayersAvailable: true,
  playerNames: ['steve', 'alex'],
  playerNamesAvailable: true,
  playerNamesPartial: false,
  plugins: ['EssentialsX', 'LuckPerms'],
  pluginsAvailable: true,
  map: 'world',
  mapAvailable: true,
  slpAvailable: true,
  queryAvailable: false,
  sourceMask: 1,
}

/** 控制台页可见页签（真实场景由能力画像推导；此处给一段有代表性的集合）。 */
const DEMO_CONSOLE_TABS: TabKey[] = ['overview', 'terminal', 'resource', 'plugins', 'metrics', 'players']

/** 页签内容插槽的说明文案（真实内容由外壳按页签注入各自取数的接线层）。 */
const DEMO_TAB_HINT: Partial<Record<TabKey, string>> = {
  overview: '概览：KPI / TPS 火花线 / 最近日志 / 动态与告警——由外壳取数后注入概览面板。',
  terminal: '终端：xterm 实例 + Worker WebSocket 直连，属应用侧接线层。',
  resource: '文件配置：关键配置面板 + 文件管理器 + 环境变量编辑器（分段壳即下一个 Panel 登记的视图）。',
  plugins: '插件：受控目录制品清单与启停入口，数据来自制品接口。',
  metrics: '监控：时序图表容器，数据来自观测接口。',
  players: '玩家：在线名单与操作入口。',
}

/* ------------------------------------------------------------------ *
 * 分区
 * ------------------------------------------------------------------ */

/** 博物馆「业务视图 · 控制台与实例」分区：受控复合组件，数据经 props 注入，组件不取数、不碰路由。 */
export function ViewsConsole() {
  // 覆盖层类视图（Sheet / Dialog）的本地开合演示态；真实场景由外壳的页面状态持有。
  const [versionDrawerOpen, setVersionDrawerOpen] = useState(false)
  const [cloneOpen, setCloneOpen] = useState(false)
  // 配置抽屉的对比起止（真实场景由外壳持有——它一变就触发 diff 取数）。
  const [diffFrom, setDiffFrom] = useState<number | null>(12)
  const [diffTo, setDiffTo] = useState<number | null>(11)
  // 数据库活动表（真实场景由外壳持有：它决定请求参数与缓存键）。
  const [dbTable, setDbTable] = useState('instances')
  // 文件配置分段：真实场景与深链 `?seg=` 解析结果同源，归外壳。
  const [segment, setSegment] = useState<ResourceSegment>('config')
  // 控制台当前页签与保活集合（真实场景归外壳：前者与 `?tab=` 深链同源、决定子组件取数）。
  const [consoleTab, setConsoleTab] = useState<TabKey>('overview')
  const [mountedTabs, setMountedTabs] = useState<TabKey[]>(['overview'])
  // Bot 分组总览的选择集与展开态（跨行共享，故归外层持有——虚拟窗口滚动会卸载行）。
  const [checkedGroups, setCheckedGroups] = useState<string[]>(['env:prod'])
  const [expandedGroup, setExpandedGroup] = useState<string | null>('env:prod')

  return (
    <>
      <Panel title="BotGroupPartsView · Bot 分组行 / 组内窥视 / 分组操作">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-10" />
              <TableHead>分组</TableHead>
              <TableHead>健康</TableHead>
              <TableHead className="text-right">总数</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {DEMO_BOT_GROUPS.map((group) => (
              <BotGroupRow
                key={group.key}
                group={group}
                checked={checkedGroups.includes(group.key)}
                onCheck={() =>
                  setCheckedGroups((prev) =>
                    prev.includes(group.key) ? prev.filter((k) => k !== group.key) : [...prev, group.key],
                  )
                }
                expanded={expandedGroup === group.key}
                onToggleExpand={() => setExpandedGroup((prev) => (prev === group.key ? null : group.key))}
                // 插槽：真实外壳注入「在控制台打开 + 单组批量菜单（接 useBotBatch）」，此处只挂操作区本体。
                actions={
                  <BotGroupActions
                    groupBy="instance"
                    onOpenInConsole={() => {}}
                    pending={false}
                    onRunBatch={() => {}}
                  />
                }
                // 插槽：真实外壳注入含 useBots 取数的分页成员容器；仅展开时调用。
                renderPeek={() => (
                  <BotGroupPeek
                    items={DEMO_PEEK_BOTS}
                    total={group.total}
                    page={2}
                    onPage={() => {}}
                    onOpenBot={() => {}}
                  />
                )}
              />
            ))}
          </TableBody>
        </Table>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：勾选与展开态由外壳持有（跨行共享，虚拟窗口滚走即卸载行）；成员分页数据经 props 注入、
          翻页只上报意图（onPage），打开 Bot 详情由外壳接弹窗；行尾操作区与窥视都是插槽——批量下发与取数属应用侧装配。
          本例给的是第 2 页样例（页大小固定 10，BOT_PEEK_PAGE_SIZE）。
        </p>
      </Panel>

      <Panel title="ConfigVersionDrawer · 配置版本抽屉（版本 / diff / 回滚）">
        <div className="grid gap-2">
          <Button variant="outline" size="sm" className="w-fit" onClick={() => setVersionDrawerOpen(true)}>
            打开配置版本抽屉
          </Button>
          <ConfigVersionDrawer
            filePath="server.properties"
            open={versionDrawerOpen}
            onOpenChange={setVersionDrawerOpen}
            onRolledBack={() => {}}
            versions={DEMO_CONFIG_VERSIONS}
            versionsLoading={false}
            versionsFailed={false}
            diffFrom={diffFrom}
            diffTo={diffTo}
            // 对比起止归外壳（它一变就触发 diff 取数）；此处就地回写以便就地演示。
            onDiffSelect={(from, to) => {
              setDiffFrom(from)
              setDiffTo(to)
            }}
            diff={DEMO_CONFIG_DIFF}
            diffLoading={false}
            onRollback={() => Promise.resolve()}
            rollbackPending={false}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：版本列表 / diff / 开合全部由外壳持有（列表与 diff 各是一次取数，开合归资源管理器）；
          组件内只留「待回滚目标」这一不触发取数的确认草稿。回滚经 onRollback 上报，成功/失败提示由外壳的 mutation 负责
          ——组件自身不发请求、不弹 toast。行内「起始 / 目标」按钮可点，对比块即随之切换；回滚按钮会先弹 DangerConfirm。
        </p>
      </Panel>

      <Panel title="InstanceConsolePageView · 服务器统一控制台页（页头 + 页签 + 保活）">
        <div className="flex h-[620px] flex-col overflow-hidden rounded-lg border">
          <InstanceConsolePageView
            instance={DEMO_CONSOLE_INSTANCE}
            node={{ name: 'node-hz-02', diskUsage: 68 }}
            metrics={DEMO_CONSOLE_METRICS}
            players={{ online: 37, maxPlayers: 60, onlineAvailable: true, maxAvailable: true }}
            mcSemantics
            visibleTabs={DEMO_CONSOLE_TABS}
            activeTab={consoleTab}
            // 页签切换归外壳：它要与 `?tab=` 深链同源，并驱动页签内容取数；此处就地演示保活并入。
            onActiveTabChange={(tab) => {
              setConsoleTab(tab)
              setMountedTabs((prev) => (prev.includes(tab) ? prev : [...prev, tab]))
            }}
            mountedTabs={mountedTabs}
            provisioning={false}
            rebuilding={false}
            canOperate
            actions={{
              start: () => {},
              rebuild: () => {},
              restart: () => {},
              stop: () => {},
              kill: () => {},
            }}
            onNotify={() => {}}
            // 插槽：真实主控台注入 react-router 的 <Link>；博物馆不依赖路由，故退化为原生 <a>。
            renderLink={({ to, children, className }) => (
              <a href={to} className={className}>
                {children}
              </a>
            )}
            // 插槽：运行态漂移横幅（自带接管 mutation 的接线组件）的真实挂载位置；此处只放位置占位。
            runtimeDriftBanner={
              <div className="rounded-md border border-dashed px-3 py-2 text-[11px] text-muted-foreground">
                runtimeDriftBanner 插槽：外壳在此注入自带「接管」写操作的漂移横幅——本例实例无漂移，故留空位。
              </div>
            }
            // renderTabPanel 插槽：各页签本体是外壳的取数接线层，故这里只能补一段说明占位。
            renderTabPanel={(tab) => (
              <div className="m-2 rounded-lg border border-dashed p-4 text-[11px] text-muted-foreground">
                {DEMO_TAB_HINT[tab] ?? '该页签内容由外壳注入。'}
              </div>
            )}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：页头指标、在线四元组、能力画像推出的可见页签与写操作回调全部注入——五个写操作（启停/重启/强杀/重建）
          一律经 actions 上报，路由链接与页签内容走插槽，故包内不碰路由、不取数；外壳注入的 onNotify 负责复制回执之类的提示
          （包内不引 sonner）。纯展示态（指标条折叠偏好、窄视口分支、页签溢出渐隐）留在组件内。点页签可见保活并入：
          访问过的页签保持挂载，仅用 &lt;Activity&gt; 隐藏、停掉轮询。
        </p>
      </Panel>

      <Panel title="InstanceResourceSegmentView · 文件配置页签（分段壳 + 三段保活）">
        <div className="h-72">
          <InstanceResourceSegmentView
            segment={segment}
            // 分段值与深链 `?seg=` 同源且决定三段各挂哪个视图，故归外壳，这里只回传。
            onSegmentChange={setSegment}
            configContent={
              <div className="space-y-2 p-3 text-[11px] text-muted-foreground">
                <p className="font-medium text-foreground">关键配置段（本例为静态样例）</p>
                <div className="grid gap-1">
                  <span>启动参数：java -Xms4G -Xmx4G -jar paper.jar nogui</span>
                  <span>max-players：40</span>
                  <span>online-mode：true</span>
                  <span>difficulty：hard</span>
                </div>
                <p>真实外壳在此注入「关键配置面板（含保存/回滚），自取数」。</p>
              </div>
            }
            filesContent={
              <div className="p-3 text-[11px] text-muted-foreground">
                文件段插槽：外壳注入文件管理器（目录树 + 编辑器），本分区下一个 Panel 登记的 FileTree 即其左栏。
              </div>
            }
            envContent={
              <div className="p-3 text-[11px] text-muted-foreground">
                环境变量段插槽：外壳注入 .env 编辑器（持未保存草稿，故与另两段一起保活）。
              </div>
            }
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：分段值归外壳（与深链同源、且决定三段各挂哪个取数视图），组件只渲染分段控件并回传切换；
          三段内容全是插槽——「何时取数、取什么」属应用侧策略。组件内保留的是保活外壳（&lt;Activity&gt; 让三段切走不丢草稿）
          这类与数据无关的呈现细节。
        </p>
      </Panel>

      <Panel title="DatabaseExplorerView · 数据库资源管理器（表树 + 行浏览）">
        <div className="flex h-80 flex-col">
          <DatabaseExplorerView
            tables={DEMO_DB_TABLES}
            tablesLoading={false}
            tablesError={false}
            activeTable={dbTable}
            // 活动表由外壳持有（它同时决定行查询的请求参数与缓存键），这里只回传。
            onSelectTable={setDbTable}
          >
            {/* 右栏内容就是下一个 Panel 登记的行视图——真实用法即此二者的组合。 */}
            <DatabaseRowsView
              query={DEMO_DB_QUERY}
              data={DEMO_DB_ROWS[dbTable]}
              isLoading={false}
              isError={false}
              isFetching={false}
              onSort={() => {}}
              onFilterColumnChange={() => {}}
              onApplyFilter={() => {}}
              onClearFilter={() => {}}
              onPageChange={() => {}}
              onPageSizeChange={() => {}}
            />
          </DatabaseExplorerView>
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：表清单（含加载/失败态）与活动表由外壳注入，组件只做左树右内容的布局与外壳；右栏经 children
          插槽注入行浏览视图，本分区下一个 Panel 就是它。点左栏表名可见切换（此处三种样例表数据各不相同）。
        </p>
      </Panel>

      <Panel title="DatabaseRowsView · 单表行浏览（过滤 + 排序 + 分页）">
        <div className="flex h-80 flex-col">
          <DatabaseRowsView
            // 查询状态（页 / 页大小 / 排序列与方向 / 过滤列）归外壳——每项变化都要重新请求。
            query={DEMO_DB_QUERY_FILTERED}
            data={DEMO_DB_ROWS.users}
            isLoading={false}
            isError={false}
            isFetching={false}
            onSort={() => {}}
            onFilterColumnChange={() => {}}
            onApplyFilter={() => {}}
            onClearFilter={() => {}}
            onPageChange={() => {}}
            onPageSizeChange={() => {}}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：查询状态与结果数据全部经 props 注入，排序 / 过滤 / 翻页 / 换页大小一律只上报意图
          ——写 URL 与发请求都由外壳执行。留在组件内的只有过滤输入草稿（不触发取数）。敏感列（password_hash）
          前端兜底打码为 ******，其列头标「敏感」且不可排序、不参与过滤列候选。
        </p>
      </Panel>

      <Panel title="FileTree · 资源管理器目录树（懒加载 + 拖放目标）">
        <div className="h-72 overflow-hidden rounded-md border">
          <FileTree
            instanceId={2}
            currentDir="plugins"
            onSelectDir={() => {}}
            onDropMove={() => {}}
            // 外部刷新信号：外壳在增删改后自增，据此重置树。
            refreshKey={0}
            // 插槽式取数：外壳注入 api 调用（本例为静态查表替身），包内不依赖应用侧 api。
            fetchEntries={demoFetchEntries}
          />
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控：目录条目不取数，而是经 fetchEntries 注入（懒加载——仅在该目录首次展开时调用）；选中、拖放、刷新信号分别经
          onSelectDir / onDropMove / refreshKey 上报或注入，真实外壳据此发请求、开文件。树内保留的是展开集合、虚拟滚动窗口
          与键盘漫游焦点这类纯浏览态。点目录可展开看子目录。
        </p>
      </Panel>

      <Panel title="CloneInstanceDialogView · 一键复制子服向导（含预检）">
        <div className="grid gap-2">
          <Button variant="outline" size="sm" className="w-fit" onClick={() => setCloneOpen(true)}>
            挂载克隆向导
          </Button>
          {/* 该视图**挂载即显示**（无 open prop）：Dialog 走 portal，无法内嵌到 Panel 里，故用按钮决定何时挂载。 */}
          {cloneOpen && (
            <CloneInstanceDialogView
              sourceName="survival-overworld"
              proxies={[
                { id: 1, name: 'survival-proxy' },
                { id: 9, name: 'creative-proxy' },
              ]}
              submitting={false}
              onClose={() => setCloneOpen(false)}
              // 预检替身：真实外壳调 dryRun 接口并负责失败提示（失败返回 null）。
              onPreview={() =>
                Promise.resolve({
                  allocated: { serverPort: 25571, queryPort: 25571, workDir: '/srv/jm/instances/survival-overworld-copy' },
                  excluded: ['logs', 'world/session.lock', 'crash-reports'],
                  warnings: ['源实例工作目录存在未纳管活进程，复制的是磁盘快照而非内存态'],
                })
              }
              // 提交替身：恒返回成功（成功才关窗，失败保留草稿便于重试）。
              onSubmit={() => Promise.resolve(true)}
            />
          )}
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：代理候选由外壳取数注入；预检与提交分别走 onPreview / onSubmit 上报——请求体组装、成功提示与告警
          都由外壳负责。全部表单草稿（名称 / motd / level-name / 代理勾选 / 复制模式 / include-exclude）与预检结果留在组件内：
          它们不触发取数。点「预检」可见预检结果块，点「复制」本例恒成功、随即关窗。
        </p>
      </Panel>

      <Panel title="InstanceRowView · 实例行（平铺表与分组树表共用）">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-10" />
              <TableHead>名称</TableHead>
              <TableHead>类型</TableHead>
              <TableHead>节点:端口</TableHead>
              <TableHead>角色</TableHead>
              <TableHead>环境与标签</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <InstanceRowView
              inst={DEMO_PROXY_INSTANCE}
              nodeName="node-hz-01"
              selected={false}
              onToggleSelect={() => {}}
              isProxy
              proxyExpanded
              onToggleProxy={() => {}}
              // 插槽：真实外壳注入含 useRegistrations 取数的后端摘要容器；仅在展开时调用。
              renderBackends={() => (
                <div className="px-4 py-2 text-[11px] text-muted-foreground">
                  后端摘要插槽：外壳在此注入「该代理已注册的后端子服列表（含优先级 / 受限标记）」。
                </div>
              )}
              menu={
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="xs" aria-label="更多操作">
                      ⋯
                    </Button>
                  </DropdownMenuTrigger>
                  {/* 插槽：真实外壳注入「标签 / 配置 / 限额 / 代理后端 / 克隆 / 接管 / 删除」各弹窗目标。 */}
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onClick={() => {}}>标签</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => {}}>配置版本</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => {}}>资源限额</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => {}}>克隆实例</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => {}}>接管进程</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => {}}>删除实例</DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              }
              onOpenInstance={() => {}}
              onStart={() => {}}
              onStop={() => {}}
              onRestart={() => {}}
              onKill={() => {}}
            />
            <InstanceRowView
              inst={DEMO_BACKEND_INSTANCE}
              nodeName="node-hz-02"
              selected
              onToggleSelect={() => {}}
              drift={{ pid: DEMO_BACKEND_INSTANCE.runtimeDriftPid ?? 0, cmdline: DEMO_BACKEND_INSTANCE.runtimeDriftCmdline }}
              menu={
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="xs" aria-label="更多操作">
                      ⋯
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onClick={() => {}}>标签</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => {}}>接管进程</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => {}}>删除实例</DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              }
              onOpenInstance={() => {}}
              onStart={() => {}}
              onStop={() => {}}
              onRestart={() => {}}
              onKill={() => {}}
            />
          </TableBody>
        </Table>
        <p className="mt-2 text-[11px] text-muted-foreground">
          受控（b 范式）：逐行渲染件不取数、不发请求、不碰路由，也不持跨行状态（虚拟窗口滚动会卸载行）——节点名、
          是否代理、搭建中、漂移、三个动作的按 id 在途态、批量勾选与 proxy 展开态全部由外壳逐行注入；启动 / 停止 / 重启 /
          强杀只上报意图（强杀由外壳弹二次确认），后端摘要与「⋯」菜单走插槽。行内自留的只有纯展示分支（按状态切主操作、
          漂移标记、就地导入徽章）。本例上行 = 运行中代理（已展开，行下挂摘要行），下行 = 已停止后端子服（勾选 + 漂移标记）。
        </p>
      </Panel>
    </>
  )
}

export default ViewsConsole
