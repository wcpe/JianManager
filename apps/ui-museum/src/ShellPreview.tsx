import { useEffect, useMemo, useState } from 'react'
import {
  AppShell,
  CommandPalette,
  ObjectPageHeader,
  PageHeader,
  PageShell,
  Panel,
  ResourceTree,
  ScopeBar,
  SideNav,
  SideNavBottom,
  SideNavHead,
  SideNavResource,
  SideNavRow,
  SideNavShortcuts,
  SummaryItem,
  SummaryStrip,
  Toolbar,
  ToolbarSpacer,
  TopNav,
  type CommandPaletteItem,
  type ResourceNode,
} from '@jianmanager/ui'
import { Activity, Network } from 'lucide-react'

/**
 * 导航外壳预览（FR-496 阶段 4 验收用）。
 *
 * 用原型同口径的假数据（12 节点 / 1,248 实例）把 AppShell + TopNav + SideNav +
 * ResourceTree + CommandPalette + 布局规范拼成一副完整可交互的外壳，用来确认
 * 「视觉方向是否对」——数据源是假的，但尺寸、结构、交互与原型一致。
 *
 * 待阶段 5 的 RouteRegistry 就绪后，本页的数据源换成真实路由与资源即可下沉为主控台外壳。
 */

/** 四个工作区（原型顶栏的中段切换项）。 */
const WORKSPACES = [
  { key: 'ops', label: '服务器运维' },
  { key: 'observability', label: '观测与自动化' },
  { key: 'operation', label: '运营与分发' },
  { key: 'platform', label: '平台管理' },
]

/**
 * 各工作区的侧栏内容——**取自 FR-496 阶段 5 的 `workspace-navigation` 真实归位**
 * （`apps/control-plane-web/src/components/console/workspace-navigation.ts`，42 条登记 = 37 导航 + 5 子路由）。
 *
 * 这里内联一份是为了让预览页不必依赖主控台的 `@/lib/roles`；等阶段 6 主控台接入新外壳后，
 * 本页即由真实数据源替代。`shortcuts` 对应原型的「三个主目的地」，`groups` 对应其余分组。
 */
const WORKSPACE_NAV: Record<
  string,
  { shortcuts: { label: string; count?: string; active?: boolean }[]; groups?: { label: string; items: string[] }[] }
> = {
  ops: {
    shortcuts: [
      { label: '集群总览' },
      { label: '全部实例', count: '1,248', active: true },
      { label: '节点', count: '12' },
    ],
    groups: [{ label: '群组与工作台', items: ['网络拓扑', '分组管理', '超级工作台', '导播台'] }],
  },
  observability: {
    shortcuts: [],
    groups: [
      { label: '观测', items: ['监控', '日志中心', '统计分析', '告警'] },
      { label: '自动化与治理', items: ['任务中心', '定时任务', '备份', '配置基线'] },
      { label: '消息', items: ['通知中心'] },
    ],
  },
  operation: {
    shortcuts: [],
    groups: [
      { label: '玩家与压测', items: ['玩家', 'Bot', '压测会话'] },
      { label: '客户端分发', items: ['客户端分发', '分发运维', '客户端发布向导'] },
    ],
  },
  platform: {
    shortcuts: [],
    groups: [
      { label: '身份与访问', items: ['用户', '用户组', '权限配置'] },
      { label: '运行时与内容', items: ['运行时资产', '模板', '探针版本库'] },
      { label: '存储与备份', items: ['存储', '文件存储配置', '备份仓库'] },
      { label: 'Agent 接入', items: ['Agent Token', 'MCP 活动', 'Agent 调用流水'] },
      { label: '系统与维护', items: ['审计日志', '设置', '数据库', '系统更新', '开源许可'] },
    ],
  },
}

/** 造 12 个节点：前两个默认展开且各有 7 条可见实例 + 隐藏计数，其余折叠。 */
function buildNodes(): ResourceNode[] {
  const nameOf = (i: number) =>
    ['lobby', 'bungee', 'login', 'survival', 'dungeon', 'minigame', 'resource'][i % 7] +
    `-${String(i + 1).padStart(2, '0')}`
  const statusOf = (i: number): ResourceNode['status'] =>
    i === 5 ? 'bad' : i === 3 ? 'warn' : 'normal'

  return [
    { id: 'node-main', name: 'node-main', count: 112 },
    { id: 'node-east-02', name: 'node-east-02', count: 103 },
    { id: 'node-east-03', name: 'node-east-03', count: 103 },
    { id: 'node-east-04', name: 'node-east-04', count: 103, status: 'warn' },
    { id: 'node-south-01', name: 'node-south-01', count: 103 },
    { id: 'node-south-02', name: 'node-south-02', count: 103 },
    { id: 'node-south-03', name: 'node-south-03', count: 103 },
    { id: 'node-west-01', name: 'node-west-01', count: 103 },
    { id: 'node-west-02', name: 'node-west-02', count: 103 },
    { id: 'node-edge-01', name: 'node-edge-01', count: 102 },
    { id: 'node-edge-02', name: 'node-edge-02', count: 102 },
    { id: 'node-edge-03', name: 'node-edge-03', count: 102 },
  ].map(({ id, name, count, status }) => ({
    id,
    name,
    status: (status ?? 'normal') as ResourceNode['status'],
    instanceCount: count,
    // 只传 instanceCount 表达总数即可——组件按 `总数 - 已列出` 自行推导「查看其余 N 个」。
    // 若同时传 hiddenCount 会被相加而重复计数（类型文档已警告）。
    instances: Array.from({ length: 7 }, (_, i) => ({
      id: `${id}-srv-${i}`,
      name: nameOf(i),
      status: statusOf(i),
    })),
  }))
}

export default function ShellPreview() {
  const [workspace, setWorkspace] = useState('ops')
  const [collapsed, setCollapsed] = useState(false)
  const [query, setQuery] = useState('')
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [tools, setTools] = useState('overview')

  const nodes = useMemo(buildNodes, [])
  const activeWorkspace = WORKSPACES.find((w) => w.key === workspace)
  const nav = WORKSPACE_NAV[workspace] ?? WORKSPACE_NAV.ops

  // ⌘K / Ctrl+K 呼出命令面板——快捷键属于外壳职责，CommandPalette 本身不监听。
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const paletteItems = useMemo<CommandPaletteItem[]>(() => {
    const pages = WORKSPACES.map((w) => ({
      id: `w-${w.key}`,
      label: w.label,
      group: '工作区',
      keywords: [w.key],
      onSelect: () => setWorkspace(w.key),
    }))
    const instances = nodes.slice(0, 3).flatMap((n) =>
      n.instances.slice(0, 3).map((i) => ({
        id: `i-${i.id}`,
        label: i.name,
        group: '实例',
        hint: n.name,
        keywords: [i.id, n.name],
        onSelect: () => setQuery(i.name),
      })),
    )
    const nodesItems = nodes.slice(0, 4).map((n) => ({
      id: `n-${n.id}`,
      label: n.name,
      group: '节点',
      hint: `${n.instanceCount} 个实例`,
      keywords: [n.id],
      onSelect: () => setQuery(n.name),
    }))
    return [...pages, ...instances, ...nodesItems]
  }, [nodes])

  return (
    <>
      <AppShell
        topbar={
          <TopNav
            brand={<span className="text-sm font-semibold tracking-tight">JianManager</span>}
            workspaces={WORKSPACES.map((w) => ({ ...w, active: w.key === workspace }))}
            onWorkspaceChange={setWorkspace}
            onSearch={() => setPaletteOpen(true)}
            sidebarCollapsed={collapsed}
            onToggleSidebar={() => setCollapsed((v) => !v)}
            actions={
              <>
                <button type="button" aria-label="全部功能" className="size-8 rounded-md text-muted-foreground hover:bg-muted hover:text-foreground">⌗</button>
                <button type="button" aria-label="通知" className="size-8 rounded-md text-muted-foreground hover:bg-muted hover:text-foreground">◔</button>
                <span className="ml-1 inline-flex size-7 items-center justify-center rounded-full bg-accent text-[10px] font-medium text-accent-foreground">JM</span>
              </>
            }
          />
        }
        sidebar={
          <SideNav collapsed={collapsed}>
            <SideNavHead pill={workspace === 'ops' ? '资源视图' : '导航'}>
              {activeWorkspace?.label}
            </SideNavHead>

            {nav.shortcuts.length > 0 && (
              <SideNavShortcuts>
                {nav.shortcuts.map((s) => (
                  <SideNavRow key={s.label} count={s.count} active={s.active}>
                    {s.label}
                  </SideNavRow>
                ))}
              </SideNavShortcuts>
            )}

            <SideNavResource>
              {workspace === 'ops' ? (
                <ResourceTree nodes={nodes} query={query} onQueryChange={setQuery} />
              ) : (
                // 非服务器运维工作区没有资源树，改为展示该工作区的分组入口（对应原型
                // 「平台管理拥有独立且清楚的五组导航」）。
                <div className="px-2 py-1">
                  {nav.groups?.map((group) => (
                    <div key={group.label} className="mb-2">
                      <div className="px-2 py-1.5 text-[11px] tracking-wide text-muted-foreground">
                        {group.label}
                      </div>
                      {group.items.map((item) => (
                        <SideNavRow key={item}>{item}</SideNavRow>
                      ))}
                    </div>
                  ))}
                </div>
              )}
            </SideNavResource>

            {workspace === 'ops' && (
              <SideNavBottom>
                <SideNavRow icon={<Network className="size-3.5" />}>群组与拓扑</SideNavRow>
                <SideNavRow icon={<Activity className="size-3.5" />}>多服工作台</SideNavRow>
              </SideNavBottom>
            )}
          </SideNav>
        }
      >
        <PageShell variant="fixed" className="overflow-auto">
          <ObjectPageHeader
            breadcrumbs={[{ label: '全部实例' }, { label: 'srv-0001' }]}
            title="srv-0001"
            status={{ tone: 'success', label: '运行中' }}
            meta={[
              { label: '所属节点', value: 'node-main' },
              { label: '类型', value: 'minecraft_java' },
            ]}
            actions={<span className="text-[11px] text-muted-foreground">外壳预览</span>}
            metrics={[
              { label: 'TPS', value: '20.0' },
              { label: '在线', value: '37' },
              { label: '内存', value: '2.4 GB' },
            ]}
            note="探针 · 2 秒前更新"
            tools={[
              { key: 'overview', label: '概览', active: tools === 'overview', onSelect: () => setTools('overview') },
              { key: 'console', label: '终端', active: tools === 'console', onSelect: () => setTools('console') },
              { key: 'files', label: '文件', active: tools === 'files', onSelect: () => setTools('files') },
            ]}
          />

          <PageHeader
            title="全部实例"
            count={1248}
            description="跨节点查找服务器，进入实例后再选择运维工具。"
            actions={<span className="text-[11px] text-muted-foreground">按 ⌘K 试试命令面板</span>}
          />

          <ScopeBar scope="全部节点" note="数据截至刚刚">
            <span className="text-[11px] text-muted-foreground">筛选：运行中 / 全部类型</span>
          </ScopeBar>

          <SummaryStrip>
            <SummaryItem label="运行中" value={1200} tone="success" />
            <SummaryItem label="已停止" value={43} />
            <SummaryItem label="维护中" value={5} tone="warning" />
            <SummaryItem label="异常" value={0} tone="danger" />
            <SummaryItem label="总计" value={1248} />
          </SummaryStrip>

          <Panel
            title="实例列表"
            actions={<span className="text-[11px] text-muted-foreground">每节点只列 7 条</span>}
            footer={<span>外壳预览 · 数据为原型口径的假数据</span>}
            bodyClassName="p-0"
          >
            <Toolbar>
              <span className="text-[11px] text-muted-foreground">工具栏（筛选 / 搜索 / 批量动作）</span>
              <ToolbarSpacer />
              <span className="text-[11px] text-muted-foreground">右对齐区</span>
            </Toolbar>
            <div className="px-3 py-6 text-center text-xs text-muted-foreground">
              页面内容区 —— 已套用阶段 3 的布局规范（PageShell / PageHeader / ScopeBar / SummaryStrip / Panel / Toolbar）
            </div>
          </Panel>
        </PageShell>
      </AppShell>

      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        items={paletteItems}
        placeholder="查找实例、节点、功能"
      />
    </>
  )
}
