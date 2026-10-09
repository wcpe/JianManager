import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Inbox, SearchX } from 'lucide-react'
import { cn } from '@jianmanager/ui'
// 这两个未进主 barrel（barrel 只含首波清单），按项目惯例走深路径。
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Skeleton } from '@jianmanager/ui/components/skeleton'

import { ThemeMatrix } from './ThemeMatrix'
import {
  Badge,
  Button,
  CardsGrid,
  Checkbox,
  Combobox,
  ContextMenuSurface,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  FieldError,
  FieldLabel,
  Input,
  Label,
  MetricCell,
  MetricComparePanel,
  MetricGrid,
  MetricsOverviewStrip,
  MiniBar,
  MonitorChart,
  MonitorSkeleton,
  NODE_CHART_DEFS,
  ObjectPageHeader,
  PageHeader,
  PageShell,
  Panel,
  PasswordInput,
  PlatformTab,
  PlatformTabs,
  RangePicker,
  ResolutionPicker,
  ResourceGauge,
  ScopeBar,
  ScrollableDialogBody,
  scrollableDialogContentClass,
  Segment,
  Segments,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SettingsLayout,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  Sparkline,
  StatCard,
  StatusBadge,
  SummaryChips,
  SummaryItem,
  SummaryStrip,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
  Textarea,
  ThreeCol,
  TimeSeriesChart,
  Toolbar,
  ToolbarSpacer,
  TwoCol,
  ViewToggle,
  type ChartSeries,
  type MetricRange,
  type MetricResolution,
  type RawSeries,
  type ViewMode,
} from '@jianmanager/ui'

const rawSeries: RawSeries[] = [
  {
    metricKey: 'node_cpu_pct',
    points: [
      { ts: '2026-07-05T00:00:00Z', value: 34 },
      { ts: '2026-07-05T00:05:00Z', value: 46 },
      { ts: '2026-07-05T00:10:00Z', value: 39 },
      { ts: '2026-07-05T00:15:00Z', value: 58 },
    ],
  },
  {
    metricKey: 'node_load',
    points: [
      { ts: '2026-07-05T00:00:00Z', value: 1.8 },
      { ts: '2026-07-05T00:05:00Z', value: 2.1 },
      { ts: '2026-07-05T00:10:00Z', value: 1.6 },
      { ts: '2026-07-05T00:15:00Z', value: 2.4 },
    ],
  },
]

const chartSeries: ChartSeries[] = [
  {
    key: 'cpu',
    name: 'CPU',
    points: rawSeries[0].points,
  },
  {
    key: 'load',
    name: 'Load',
    points: rawSeries[1].points,
  },
]

/** 博物馆用的演示数据源：受控组件不自行取数，数据经 source 注入。 */

/**
 * 博物馆分区清单 —— 侧边栏按它渲染，一次只展示一个分区。
 *
 * 为什么分批而不是一长页：这是**组件库/控件博物馆**，用途是「按需查某个控件长什么样」；
 * 全部件堆在一页会滚很久、也难以定位。旧版即是一长页（且把应用级外壳也塞进来当展品）。
 */
const SECTIONS = [
  { id: 'foundation', label: 'Foundation', hint: '设计 token 与色板' },
  { id: 'themes', label: '主题矩阵', hint: '5 主题 × 明暗' },
  { id: 'actions', label: 'Actions', hint: '按钮与操作触发件' },
  { id: 'forms', label: 'Forms', hint: '输入、选择与表单字段' },
  { id: 'data', label: 'Data', hint: '表格、卡片、统计与图谱' },
  { id: 'overlay', label: 'Overlay', hint: '对话框与 Sheet' },
  { id: 'monitoring', label: 'Monitoring', hint: '图表与指标条' },
  { id: 'tabs', label: 'Tabs', hint: '页签' },
  { id: 'layout', label: '布局', hint: '页面壳与布局原语' },
] as const

type SectionId = (typeof SECTIONS)[number]['id']

/** 分区容器：只有当前分区才渲染子树（分批展示，不把九套样例同时挂进 DOM）。 */
function Section({
  id,
  active,
  title,
  hint,
  children,
}: {
  id: SectionId
  active: SectionId
  title: string
  hint: string
  children: ReactNode
}) {
  if (id !== active) return null
  return (
    <section className="grid gap-4">
      <div className="border-b pb-3">
        <h2 className="text-lg font-semibold">{title}</h2>
        <p className="mt-1 text-xs text-muted-foreground">{hint}</p>
      </div>
      <div className="grid gap-3">{children}</div>
    </section>
  )
}

export default function App() {
  const [range, setRange] = useState<MetricRange>('24h')
  const [mode, setMode] = useState<ViewMode>('list')
  const [dark, setDark] = useState(false)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [sheetOpen, setSheetOpen] = useState(false)
  const [scrollDialogOpen, setScrollDialogOpen] = useState(false)
  const [comboValue, setComboValue] = useState('survival-01')
  const [menuPos, setMenuPos] = useState<{ x: number; y: number } | null>(null)
  const [resolution, setResolution] = useState<MetricResolution>('auto')
  const [compareSel, setCompareSel] = useState<string[]>([])
  const toggleCompare = (k: string) =>
    setCompareSel((prev) => (prev.includes(k) ? prev.filter((x) => x !== k) : [...prev, k]))
  const chips = useMemo(
    () => [
      { key: 'online', label: '在线', count: 8, level: 'success' as const, breathing: true },
      { key: 'warn', label: '维护', count: 2, level: 'warning' as const },
      { key: 'down', label: '离线', count: 1, level: 'danger' as const },
    ],
    [],
  )

  const [section, setSection] = useState<SectionId>('foundation')

  return (
    <main className={dark ? 'dark flex h-screen flex-col bg-background text-foreground' : 'flex h-screen flex-col bg-background text-foreground'}>
      <header className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b px-5 py-3">
        <div>
          <h1 className="text-lg font-bold">JianManager 控件博物馆</h1>
          <p className="mt-0.5 text-xs text-muted-foreground">@jianmanager/ui · 按分类查看控件</p>
        </div>
        <Button size="sm" variant="outline" onClick={() => setDark((v) => !v)}>
          {dark ? '亮色' : '暗色'}
        </Button>
      </header>

      <div className="flex min-h-0 flex-1">
        {/* 分类导航：一次只展示一个分类的控件，避免全部件堆成一长页 */}
        <nav aria-label="控件分类" className="w-60 shrink-0 overflow-y-auto border-r bg-card/40 p-2">
          {SECTIONS.map((s) => (
            <button
              key={s.id}
              type="button"
              aria-current={section === s.id ? 'page' : undefined}
              onClick={() => setSection(s.id)}
              className={cn(
                'block w-full rounded-md px-3 py-2 text-left text-sm transition-colors',
                section === s.id
                  ? 'bg-accent font-medium text-accent-foreground'
                  : 'text-muted-foreground hover:bg-muted hover:text-foreground',
              )}
            >
              {s.label}
              <span className="mt-0.5 block text-[11px] font-normal opacity-70">{s.hint}</span>
            </button>
          ))}
        </nav>

        <div className="min-h-0 flex-1 overflow-y-auto p-5">
          <div className="mx-auto grid max-w-5xl gap-6">
            <Section id="foundation" active={section} title="Foundation" hint="设计 token 与色板">
              <Panel title="Token">
                <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">
                  {['primary', 'card', 'muted', 'success', 'danger'].map((name) => (
                    <div key={name} className="rounded-md border bg-card p-2">
                      <div
                        className="h-8 rounded"
                        style={{ background: name === 'success' ? 'var(--status-success)' : name === 'danger' ? 'var(--status-danger)' : `var(--${name})` }}
                      />
                      <p className="mt-2 text-xs text-muted-foreground">{name}</p>
                    </div>
                  ))}
                </div>
              </Panel>
            </Section>

        {/* 主题矩阵：阶段 7 的验证台。外壳预览已移除——它展示的是应用级骨架
            （AppShell/TopNav/SideNav）而非可复用的控件，不属于本博物馆的展品范围。 */}
        <Section id="themes" active={section} title="主题矩阵" hint="5 套主题色 × 明暗，控件随变量实时跟变">
          <ThemeMatrix />
        </Section>

        <Section id="actions" active={section} title="Actions" hint="按钮与操作触发件">
          <Panel title="Button">
            <div className="flex flex-wrap items-center gap-2">
              <Button>主操作</Button>
              <Button variant="outline">次操作</Button>
              <Button variant="ghost">弱操作</Button>
              <Button variant="destructive">危险操作</Button>
              <Button disabled>禁用</Button>
            </div>
          </Panel>
        </Section>

        <Section id="forms" active={section} title="Forms" hint="输入、选择与表单字段">
          <Panel title="Inputs">
            <div className="grid gap-3 md:grid-cols-2">
              <label className="grid gap-1">
                <FieldLabel required>服务器名称</FieldLabel>
                <Input defaultValue="survival-01" />
              </label>
              <label className="grid gap-1">
                <FieldLabel>访问密钥</FieldLabel>
                <PasswordInput defaultValue="jianmanager" />
              </label>
              <label className="grid gap-1">
                <FieldLabel>节点</FieldLabel>
                <Select defaultValue="edge-a">
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="edge-a">edge-a</SelectItem>
                    <SelectItem value="edge-b">edge-b</SelectItem>
                  </SelectContent>
                </Select>
              </label>
              <label className="grid gap-1">
                <FieldLabel>备注</FieldLabel>
                <Textarea defaultValue="压测窗口保留 2 小时" />
              </label>
              <label className="flex items-center gap-2">
                <Checkbox defaultChecked />
                <span>开启维护窗口</span>
              </label>
              <FieldError error="端口范围不可为空" />
            </div>
          </Panel>

          <Panel title="Combobox · 可编辑下拉">
            <div className="max-w-64">
              <Combobox
                options={[
                  { value: 'survival-01' },
                  { value: 'survival-02' },
                  { value: 'lobby-01', label: 'lobby-01（大厅）' },
                ]}
                value={comboValue}
                onChange={setComboValue}
                placeholder="选择或输入实例名"
                ariaLabel="实例选择示例"
              />
            </div>
          </Panel>

          <Panel title="Label · 表单标签">
            <div className="grid max-w-64 gap-1.5">
              <Label htmlFor="demo-host">主机名</Label>
              <Input id="demo-host" defaultValue="node-east-01" />
            </div>
          </Panel>
        </Section>

        <Section id="data" active={section} title="Data" hint="表格、卡片、统计与图谱">
          <div className="grid gap-3 lg:grid-cols-3">
            <StatCard label="在线节点" value="8/10" sub="集群" bar={{ value: 80, level: 'success' }} />
            <StatCard label="CPU" value="58%" sub="平均" bar={{ value: 58, level: 'warning' }} />
            <Panel title="状态">
              <div className="flex flex-wrap gap-2">
                <Badge>default</Badge>
                <StatusBadge level="success" label="RUNNING" />
                <StatusBadge level="warning" label="STARTING" />
                <StatusBadge level="danger" label="CRASHED" />
              </div>
            </Panel>
          </div>
          <Panel title="Table" bodyClassName="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>名称</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>水位</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {['lobby', 'survival', 'proxy'].map((name, index) => (
                  <TableRow key={name}>
                    <TableCell className="font-medium">{name}</TableCell>
                    <TableCell>
                      <StatusBadge level={index === 1 ? 'warning' : 'success'} label={index === 1 ? '维护中' : '运行中'} />
                    </TableCell>
                    <TableCell>
                      <MiniBar value={42 + index * 18} className="w-24" />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Panel>
          <SummaryChips chips={chips} />
          <ViewToggle value={mode} onChange={setMode} cardLabel="卡片视图" listLabel="列表视图" />

          <Panel title="EmptyState · 空态">
            <div className="grid gap-3 lg:grid-cols-2">
              <div className="rounded-md border">
                <EmptyState icon={<Inbox />} title="暂无实例" description="创建第一个实例后这里会显示它。" />
              </div>
              <div className="rounded-md border">
                <EmptyState
                  icon={<SearchX />}
                  title="没有匹配的节点"
                  action={
                    <Button size="sm" variant="outline">
                      清除筛选
                    </Button>
                  }
                />
              </div>
            </div>
          </Panel>

          <Panel title="Skeleton · 骨架屏">
            <div className="grid items-center gap-4 lg:grid-cols-[1fr_1fr_auto]">
              <div className="grid gap-2">
                <Skeleton className="h-4 w-2/3" />
                <Skeleton className="h-3 w-full" />
                <Skeleton className="h-3 w-4/5" />
              </div>
              <Skeleton className="h-20 rounded-lg" />
              <Skeleton className="size-12 rounded-full" />
            </div>
          </Panel>
        </Section>

        <Section id="overlay" active={section} title="Overlay" hint="对话框与 Sheet">
          <Panel title="Dialog / Sheet / Menu">
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => setDialogOpen(true)}>Dialog</Button>
              <Button variant="outline" onClick={() => setSheetOpen(true)}>Sheet</Button>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline">Dropdown</Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent>
                  <DropdownMenuItem>刷新</DropdownMenuItem>
                  <DropdownMenuItem>复制 ID</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </Panel>

          <Panel title="ScrollableDialog · 头脚固定、仅正文滚动">
            <div className="grid gap-2">
              <p className="text-[11px] text-muted-foreground">
                表单类模态的强制形态（见 .claude/rules/ui-modals.md）：DialogContent 套
                scrollableDialogContentClass，正文包 ScrollableDialogBody，小屏下底部按钮不被挤出。
              </p>
              <Button variant="outline" className="w-fit" onClick={() => setScrollDialogOpen(true)}>
                打开可滚动对话框
              </Button>
            </div>
          </Panel>

          <Panel title="ContextMenuSurface · 右键菜单浮层基座">
            <div className="grid gap-2">
              <div
                className="grid h-24 cursor-context-menu place-items-center rounded-md border border-dashed text-[11px] text-muted-foreground"
                onContextMenu={(e) => {
                  e.preventDefault()
                  setMenuPos({ x: e.clientX, y: e.clientY })
                }}
              >
                在此右键
              </div>
              <p className="text-[11px] text-muted-foreground">
                portal 到 body 并做视口钳制——控制台外壳带 transform 会劫持 fixed 包含块，
                不 portal 的浮层会整体漂移。
              </p>
            </div>
            {menuPos !== null && (
              <ContextMenuSurface
                x={menuPos.x}
                y={menuPos.y}
                className="min-w-[160px] rounded-md border bg-popover p-1 shadow-md"
              >
                <button
                  type="button"
                  className="block w-full rounded px-2 py-1.5 text-left text-xs hover:bg-accent"
                  onClick={() => setMenuPos(null)}
                >
                  menu surface（portal + 钳制）
                </button>
              </ContextMenuSurface>
            )}
          </Panel>
          <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>节点操作</DialogTitle>
                <DialogDescription>确认后会进入维护窗口。</DialogDescription>
              </DialogHeader>
            </DialogContent>
          </Dialog>
          <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
            <SheetContent>
              <SheetHeader>
                <SheetTitle>侧边详情</SheetTitle>
                <SheetDescription>展示紧凑状态与操作。</SheetDescription>
              </SheetHeader>
            </SheetContent>
          </Sheet>

          <Dialog open={scrollDialogOpen} onOpenChange={setScrollDialogOpen}>
            <DialogContent className={scrollableDialogContentClass}>
              <DialogHeader>
                <DialogTitle>编辑分组（可滚动）</DialogTitle>
                <DialogDescription>头部与底部固定，仅正文滚动。</DialogDescription>
              </DialogHeader>
              <ScrollableDialogBody className="space-y-3">
                {Array.from({ length: 10 }, (_, i) => (
                  <div key={i} className="grid gap-1.5">
                    <Label htmlFor={`sd-${i}`}>字段 {i + 1}</Label>
                    <Input id={`sd-${i}`} defaultValue={`value-${i + 1}`} />
                  </div>
                ))}
              </ScrollableDialogBody>
            </DialogContent>
          </Dialog>
        </Section>

        <Section id="monitoring" active={section} title="Monitoring" hint="图表与指标条">
          <Panel title="Range">
            <RangePicker value={range} onChange={setRange} />
          </Panel>
          <div className="grid gap-3 lg:grid-cols-[220px_1fr]">
            <Panel title="Gauge">
              <div className="flex items-center gap-4">
                <ResourceGauge label="CPU" value={58} unit="%" />
                <div className="h-10 flex-1">
                  <Sparkline points={rawSeries[0].points} ariaLabel="CPU trend" />
                </div>
              </div>
            </Panel>
            <Panel title="Chart">
              <TimeSeriesChart series={chartSeries} height={180} valueFormatter={(v) => v.toFixed(1)} />
            </Panel>
          </div>
          <Panel title="MonitorChart">
            <MonitorChart series={chartSeries} height={180} valueFormatter={(v) => v.toFixed(1)} />
          </Panel>
          <Panel title="MetricsOverviewStrip">
            <MetricsOverviewStrip kind="node" raw={rawSeries} isLoading={false} />
          </Panel>

          <Panel title="ResolutionPicker · 聚合粒度">
            <ResolutionPicker value={resolution} onChange={setResolution} />
          </Panel>

          <Panel title="TimeSeriesChart · 参考线">
            <TimeSeriesChart
              series={chartSeries}
              height={180}
              valueFormatter={(v) => v.toFixed(1)}
              referenceLines={[{ value: 50, label: '阈值 50' }]}
            />
          </Panel>

          <Panel title="MetricComparePanel · 多指标叠加对比">
            <MetricComparePanel
              kind="node"
              raw={rawSeries}
              selected={compareSel}
              onToggle={toggleCompare}
              height={180}
            />
          </Panel>

          <Panel title="MonitorSkeleton · 监控加载骨架">
            <MonitorSkeleton
              defs={NODE_CHART_DEFS.slice(0, 3)}
              source={{ kind: 'node', uuid: 'alpha' }}
              chartHeight={110}
              useSeries={() => ({ series: [], isLoading: true })}
            />
          </Panel>
        </Section>

        <Section id="tabs" active={section} title="Tabs" hint="页签">
          <Tabs defaultValue="light">
            <TabsList>
              <TabsTrigger value="light">亮色</TabsTrigger>
              <TabsTrigger value="dark">暗色</TabsTrigger>
            </TabsList>
            <TabsContent value="light">默认 A 亮色高密度主题。</TabsContent>
            <TabsContent value="dark">后续 B 暗色主题基调。</TabsContent>
          </Tabs>
        </Section>

        {/* 布局层（FR-496 阶段 3 + 阶段 6 收尾）：此前一处都没收录，页面迁移依赖它却无从在此核对。
            它是「一页内部怎么摆」的唯一出处——页面只从这里取原语，不再手写 space-y / grid-cols 骨架类名。 */}
        <Section id="layout" active={section} title="布局" hint="页面壳与布局原语">
          <p className="text-[11px] text-muted-foreground">
            内容页骨架的唯一出处：任何内容页的第一个子元素都是 PageHeader（或详情页的 ObjectPageHeader）。
          </p>

          <Panel title="PageShell · 三种壳态">
            <div className="grid gap-2">
              {(
                [
                  ['default', '整页滚动 · 大多数内容页'],
                  ['fixed', '固定视口、内部区域自行滚动 · 列表页 / 设置页'],
                  ['tool', '全占满、无外层留白 · 终端 / 文件等自带滚动的工具页'],
                ] as const
              ).map(([variant, hint]) => (
                <div key={variant} className="overflow-hidden rounded-md border">
                  {/* PageShell 自带 flex-1 与滚动模型，故用定高盒承载，否则会撑满整页 */}
                  <div className="flex h-14 flex-col">
                    <PageShell variant={variant}>
                      <span className="text-[11px] text-muted-foreground">
                        variant=<code>{variant}</code> · {hint}
                      </span>
                    </PageShell>
                  </div>
                </div>
              ))}
            </div>
          </Panel>

          <Panel title="PageHeader · 列表 / 首页页头">
            <PageHeader
              title="实例"
              count={1248}
              description="跨节点查找服务器，进入实例后再选择运维工具。"
              actions={<Button size="sm">创建实例</Button>}
            />
          </Panel>

          <Panel title="ObjectPageHeader · 详情页对象头">
            <ObjectPageHeader
              breadcrumbs={[{ label: '实例', to: '/instances' }, { label: 'survival-01' }]}
              title="survival-01"
              status={{ tone: 'success', label: '运行中' }}
              meta={[
                { label: '节点', value: 'alpha' },
                { label: '端口', value: ':25565' },
                { label: 'UUID', value: 'a1b2c3d4' },
              ]}
              actions={<Button size="sm">重启</Button>}
              metrics={[
                { label: 'TPS', value: '19.8' },
                { label: '在线玩家', value: '42 / 100' },
                { label: 'CPU', value: '38%', tone: 'warning' },
              ]}
              note="探针 · 2 秒前更新"
              tools={[
                { key: 'overview', label: '概览', active: true },
                { key: 'monitor', label: '监控' },
                { key: 'console', label: '终端' },
                { key: 'files', label: '文件' },
              ]}
              toolsLabel="实例工具"
              // 博物馆内不真的跳转，只演示面包屑链接外观
              onNavigate={() => {}}
            />
          </Panel>

          <Panel title="Segments · 分段控件（同一份数据的少数几种呈现）">
            <div className="grid gap-2">
              <Segments aria-label="节点视图">
                <Segment active>活跃</Segment>
                <Segment>归档</Segment>
              </Segments>
              <Segments className="w-full" aria-label="节点视图（撑满父宽）">
                <Segment className="flex-1" active>
                  活跃
                </Segment>
                <Segment className="flex-1">归档</Segment>
              </Segments>
            </div>
          </Panel>

          <Panel title="PlatformTabs · 工作区页签">
            <PlatformTabs aria-label="平台管理分组">
              <PlatformTab active>身份与访问</PlatformTab>
              <PlatformTab>运行时与内容</PlatformTab>
              <PlatformTab>系统与数据</PlatformTab>
            </PlatformTabs>
          </Panel>

          <Panel title="ScopeBar / SummaryStrip / Toolbar">
            <div className="grid gap-2">
              <ScopeBar scope="全部节点" note="数据截至 12:00" />
              <SummaryStrip>
                <SummaryItem label="运行实例" value={63} />
                <SummaryItem label="已停止" value={128} />
                <SummaryItem label="已崩溃" value={4} tone="danger" />
                <SummaryItem label="状态待确认" value={2} tone="warning" />
                <SummaryItem label="在线节点" value="11 / 12" />
              </SummaryStrip>
              <Toolbar className="rounded-md border">
                <span className="text-[11px] text-muted-foreground">工具条：筛选 / 搜索 / 批量动作</span>
                <ToolbarSpacer />
                <Button size="sm" variant="outline">
                  刷新
                </Button>
              </Toolbar>
            </div>
          </Panel>

          <Panel title="网格族">
            <div className="grid gap-3">
              <TwoCol>
                <div className="rounded-md border bg-muted/40 p-3 text-[11px]">TwoCol · 主内容</div>
                <div className="rounded-md border bg-muted/40 p-3 text-[11px]">TwoCol · 侧栏</div>
              </TwoCol>
              <ThreeCol>
                {[1, 2, 3].map((n) => (
                  <div key={n} className="rounded-md border bg-muted/40 p-3 text-[11px]">
                    ThreeCol · {n}
                  </div>
                ))}
              </ThreeCol>
              <MetricGrid>
                {[
                  ['PID', '21846'],
                  ['CPU', '38%'],
                  ['线程', '52'],
                  ['已运行', '2 天 4 小时'],
                ].map(([k, v]) => (
                  <MetricCell key={k}>
                    <span className="block text-[10px] text-muted-foreground">{k}</span>
                    <span className="font-mono text-lg">{v}</span>
                  </MetricCell>
                ))}
              </MetricGrid>
              <CardsGrid className="max-h-36">
                {[1, 2, 3].map((n) => (
                  <div key={n} className="rounded-lg border bg-card p-3 text-[11px]">
                    CardsGrid · 卡片 {n}
                  </div>
                ))}
              </CardsGrid>
              <SettingsLayout>
                <nav className="rounded-lg border bg-card p-2 text-[11px] text-muted-foreground">
                  SettingsLayout · 170px 左栏
                </nav>
                <div className="rounded-lg border bg-card p-3 text-[11px]">SettingsLayout · 内容区</div>
              </SettingsLayout>
            </div>
          </Panel>
        </Section>


          </div>
        </div>
      </div>
    </main>
  )
}
