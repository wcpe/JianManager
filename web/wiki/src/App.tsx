import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Badge,
  Button,
  Checkbox,
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
  MetricsOverviewStrip,
  MiniBar,
  MonitorChart,
  Panel,
  PasswordInput,
  RangePicker,
  ResourceGauge,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  Sparkline,
  StatCard,
  StatusBadge,
  SummaryChips,
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
  TimeSeriesChart,
  ViewToggle,
  type ChartSeries,
  type MetricRange,
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

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-3 border-t py-5 md:grid-cols-[180px_1fr]">
      <div>
        <h2 className="text-sm font-semibold">{title}</h2>
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
  const chips = useMemo(
    () => [
      { key: 'online', label: '在线', count: 8, level: 'success' as const, breathing: true },
      { key: 'warn', label: '维护', count: 2, level: 'warning' as const },
      { key: 'down', label: '离线', count: 1, level: 'danger' as const },
    ],
    [],
  )

  return (
    <main className={dark ? 'dark min-h-screen bg-background text-foreground' : 'min-h-screen bg-background text-foreground'}>
      <div className="mx-auto max-w-6xl px-5 py-5">
        <header className="flex flex-wrap items-center justify-between gap-3 pb-3">
          <div>
            <h1 className="text-xl font-bold">JianManager 控件博物馆</h1>
            <p className="mt-1 text-xs text-muted-foreground">@jianmanager/ui · A+C 高密度运维控件</p>
          </div>
          <Button size="sm" variant="outline" onClick={() => setDark((v) => !v)}>
            {dark ? '亮色' : '暗色'}
          </Button>
        </header>

        <Section title="Foundation">
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

        <Section title="Actions">
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

        <Section title="Forms">
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
        </Section>

        <Section title="Data">
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
        </Section>

        <Section title="Overlay">
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
        </Section>

        <Section title="Monitoring">
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
        </Section>

        <Section title="Tabs">
          <Tabs defaultValue="light">
            <TabsList>
              <TabsTrigger value="light">亮色</TabsTrigger>
              <TabsTrigger value="dark">暗色</TabsTrigger>
            </TabsList>
            <TabsContent value="light">默认 A 亮色高密度主题。</TabsContent>
            <TabsContent value="dark">后续 B 暗色主题基调。</TabsContent>
          </Tabs>
        </Section>
      </div>
    </main>
  )
}
