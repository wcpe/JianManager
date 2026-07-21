import { useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router'
import { ShieldAlert, ShieldCheck, Ban, Gauge, RadioTower, UsersRound } from 'lucide-react'
import { toast } from 'sonner'
import {
  useBlockClientDistIP,
  useCancelClientDistIPBlock,
  useClearClientDistChannelProtection,
  useClientDistIpAnalysis,
  useClientDistPlayerAnalysis,
  useClientDistSecurityActions,
  useClientDistSecurityEvents,
  useClientDistSecurityLogs,
  useClientDistSecurityOverview,
  useClientDistSecurityProfile,
  useClientDistSecurityProfiles,
  useClientSecurityGroups,
  useCreateClientSecurityGroup,
  useDeleteClientSecurityGroup,
  useSetClientDistChannelProtection,
  useSetClientDistKeyState,
  type ChannelProtectionMode,
  type ClientDistSecurityEvent,
  type ClientDistSecurityLogType,
  type ClientProtectionAction,
  type KeySecurityState,
  type SecurityLevel,
  type SecurityTargetType,
} from '@/api/clientDistSecurity'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'
import { Textarea } from '@jianmanager/ui/components/textarea'
import DangerConfirm from '@/components/DangerConfirm'
import UntrustedFieldBadge from '@/components/UntrustedFieldBadge'
import { buildClientDistHref, readClientDistQuery, updateClientDistQuery, type ClientDistQueryKey } from '@/lib/client-dist-query'
import { useTabParam } from '@/lib/use-tab-param'
import { maskInstallId, maskMachineId, maskPlayerName } from '@/lib/privacy-mask'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'

const EMPTY = '—'

type SecurityQueryPatch = Partial<Record<ClientDistQueryKey, string | null>>

function useSecurityQuery() {
  const [searchParams, setSearchParams] = useSearchParams()
  return {
    query: readClientDistQuery(searchParams),
    updateQuery: (patch: SecurityQueryPatch) => {
      setSearchParams(updateClientDistQuery(searchParams, patch), { replace: true })
    },
  }
}

function fmtTime(iso?: string | null): string {
  if (!iso) return EMPTY
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

function fmtBytes(bytes?: number): string {
  const b = Number(bytes ?? 0)
  if (!Number.isFinite(b) || b <= 0) return '0 B'
  if (b >= 1024 ** 3) return `${(b / 1024 ** 3).toFixed(1)} GiB`
  if (b >= 1024 ** 2) return `${(b / 1024 ** 2).toFixed(1)} MiB`
  if (b >= 1024) return `${(b / 1024).toFixed(1)} KiB`
  return `${b} B`
}

function levelVariant(level?: SecurityLevel): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (level === 'critical' || level === 'high') return 'destructive'
  if (level === 'warn') return 'default'
  return 'secondary'
}

function statusVariant(status?: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (status === 'active' || status === 'suspended' || status === 'revoked') return 'destructive'
  if (status === 'throttled' || status === 'observe') return 'default'
  if (status === 'canceled' || status === 'expired') return 'outline'
  return 'secondary'
}

function EmptyState({ text }: { text: string }) {
  return <p className="py-10 text-center text-sm text-muted-foreground">{text}</p>
}

function TrustNotice() {
  return (
    <div className="flex gap-2 rounded-lg border border-status-warning/35 bg-status-warning/10 px-4 py-3 text-sm text-foreground shadow-soft">
      <ShieldAlert className="mt-0.5 size-4 shrink-0 text-status-warning" />
      <span>
        玩家名、machineId、installId 均由客户端上报，可能被伪造或篡改；它们仅用于画像、分组与人工研判，不能作为可信授权或唯一封禁主键。
      </span>
    </div>
  )
}

function KpiCard({ title, value, hint }: { title: string; value: string | number; hint: string }) {
  return (
    <div className="rounded-lg border bg-card/95 p-4 shadow-soft">
      <div className="text-xs font-medium text-muted-foreground">{title}</div>
      <div className="mt-2 text-2xl font-semibold tabular-nums">{value}</div>
      <div className="mt-1 text-xs text-muted-foreground">{hint}</div>
    </div>
  )
}

function RankList({
  title,
  items,
  filterKey,
}: {
  title: string
  items: { subject: string; count: number; bytes?: number }[]
  filterKey?: 'ip' | 'channelId'
}) {
  const [searchParams] = useSearchParams()
  return (
    <Panel title={title}>
      {items.length === 0 ? (
        <EmptyState text="暂无排行数据" />
      ) : (
        <ul className="space-y-2">
          {items.slice(0, 8).map((item) => (
            <li key={item.subject} className="flex items-center justify-between gap-3 rounded-md border bg-muted/25 px-3 py-2 text-sm">
              {filterKey ? (
                <Link
                  className="min-w-0 truncate font-medium text-primary hover:underline"
                  to={buildClientDistHref('/client-dist-monitor', searchParams, { [filterKey]: item.subject, tab: 'logs' })}
                >
                  {item.subject || EMPTY}
                </Link>
              ) : (
                <span className="min-w-0 truncate font-medium">{item.subject || EMPTY}</span>
              )}
              <span className="shrink-0 text-muted-foreground">
                {item.count} 次{item.bytes ? ` · ${fmtBytes(item.bytes)}` : ''}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

function OverviewTab() {
  const { data, isError, isLoading } = useClientDistSecurityOverview()
  if (isError) return <EmptyState text="安全总览接口暂不可用，请确认后端 /client-dist/security/overview 已落地。" />
  const overview = data
  return (
    <div className="space-y-4">
      <TrustNotice />
      <div className="grid gap-3 md:grid-cols-3 xl:grid-cols-6">
        <KpiCard title="活跃下载" value={overview?.activeDownloads ?? (isLoading ? '…' : 0)} hint="当前下载并发" />
        <KpiCard title="下载带宽" value={fmtBytes(overview?.downloadBytesPerSecond)} hint="近实时吞吐 / 秒" />
        <KpiCard title="异常请求" value={overview?.abnormalRequests ?? 0} hint="风险事件与异常请求" />
        <KpiCard title="401 / 403 / 429" value={`${overview?.unauthorizedRequests ?? 0}/${overview?.forbiddenRequests ?? 0}/${overview?.rateLimitedRequests ?? 0}`} hint="鉴权、拒绝、限流" />
        <KpiCard title="封禁 IP" value={overview?.blockedIpCount ?? 0} hint="当前生效临时封禁" />
        <KpiCard title="保护对象" value={`${overview?.throttledKeyCount ?? 0}/${overview?.protectedChannelCount ?? 0}`} hint="限速 key / 保护频道" />
      </div>
      <div className="grid gap-4 lg:grid-cols-2 xl:grid-cols-4">
        <RankList title="Top IP" items={overview?.topIps ?? []} filterKey="ip" />
        <RankList title="Top Key" items={overview?.topKeys ?? []} />
        <RankList title="Top Channel" items={overview?.topChannels ?? []} filterKey="channelId" />
        <RankList title="Top 玩家名" items={overview?.topPlayers ?? []} />
      </div>
    </div>
  )
}

const logTypeLabels: Record<ClientDistSecurityLogType | 'all', string> = {
  all: '全部类型',
  hello: 'Security Hello',
  risk: '风险事件',
  action: '保护动作',
  request: '请求日志',
  runtime: '运行态心跳',
  telemetry: '更新遥测',
}

function LogsTab() {
  const { query, updateQuery } = useSecurityQuery()
  const [type, setType] = useState<ClientDistSecurityLogType | 'all'>('all')
  const [playerName, setPlayerName] = useState('')
  const { data, isError, isLoading } = useClientDistSecurityLogs({
    type,
    channelId: query.channelId,
    machineId: query.machineId,
    playerName: playerName || undefined,
    ip: query.ip,
    errCode: query.errCode,
    page: 1,
    pageSize: 100,
  })
  const items = data?.items ?? []
  return (
    <Panel
      title="全量日志详情"
      actions={
        <div className="flex flex-wrap gap-2">
          <Select value={type} onValueChange={(v) => setType(v as ClientDistSecurityLogType | 'all')}>
            <SelectTrigger className="w-36"><SelectValue /></SelectTrigger>
            <SelectContent>
              {(Object.keys(logTypeLabels) as Array<ClientDistSecurityLogType | 'all'>).map((key) => (
                <SelectItem key={key} value={key}>{logTypeLabels[key]}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input className="w-36" placeholder="频道" value={query.channelId ?? ''} onChange={(e) => updateQuery({ channelId: e.target.value || null })} />
          <Input className="w-40" placeholder="Machine ID" value={query.machineId ?? ''} onChange={(e) => updateQuery({ machineId: e.target.value || null })} />
          <Input className="w-32" placeholder="玩家名" value={playerName} onChange={(e) => setPlayerName(e.target.value)} />
          <Input className="w-36" placeholder="IP" value={query.ip ?? ''} onChange={(e) => updateQuery({ ip: e.target.value || null })} />
          <Input className="w-40" placeholder="错误码" value={query.errCode ?? ''} onChange={(e) => updateQuery({ errCode: e.target.value || null })} />
        </div>
      }
    >
      {isError ? (
        <EmptyState text="全量日志接口暂不可用。" />
      ) : items.length === 0 ? (
        <EmptyState text={isLoading ? '加载中…' : '暂无日志'} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>时间</TableHead>
              <TableHead>类型</TableHead>
              <TableHead>摘要</TableHead>
              <TableHead>对象</TableHead>
              <TableHead>状态 / 错误</TableHead>
              <TableHead>详情</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((item) => (
              <TableRow key={item.id}>
                <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(item.createdAt)}</TableCell>
                <TableCell><Badge variant="outline">{logTypeLabels[item.type]}</Badge></TableCell>
                <TableCell className="font-medium">{item.title || EMPTY}</TableCell>
                <TableCell className="max-w-64 text-xs">
                  <div className="truncate">频道：{item.channelId || EMPTY}</div>
                  <div className="truncate text-muted-foreground">玩家：{item.playerName || EMPTY} · IP：{item.ip || EMPTY}</div>
                  <div className="truncate text-muted-foreground">机器：{item.machineId || EMPTY}</div>
                </TableCell>
                <TableCell>
                  <div>{item.status || EMPTY}</div>
                  <div className="text-xs text-muted-foreground">{item.errCode || EMPTY}</div>
                </TableCell>
                <TableCell className="max-w-96">
                  <pre className="max-h-40 overflow-auto rounded bg-muted p-2 text-xs text-muted-foreground">{JSON.stringify(item.detail ?? {}, null, 2)}</pre>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

function EventsTab() {
  const { query, updateQuery } = useSecurityQuery()
  const [riskRule, setRiskRule] = useState('')
  const { data, isError, isLoading } = useClientDistSecurityEvents({
    channelId: query.channelId,
    ip: query.ip,
    machineId: query.machineId,
    errCode: query.errCode,
    riskRule: riskRule || undefined,
    limit: 200,
  })
  const events = data ?? []
  return (
    <Panel
      title="异常请求分析"
      actions={
        <div className="flex flex-wrap gap-2">
          <Input className="w-36" placeholder="频道" value={query.channelId ?? ''} onChange={(e) => updateQuery({ channelId: e.target.value || null })} />
          <Input className="w-36" placeholder="IP" value={query.ip ?? ''} onChange={(e) => updateQuery({ ip: e.target.value || null })} />
          <Input className="w-40" placeholder="错误码" value={query.errCode ?? ''} onChange={(e) => updateQuery({ errCode: e.target.value || null })} />
          <Input className="w-40" placeholder="风险规则" value={riskRule} onChange={(e) => setRiskRule(e.target.value)} />
        </div>
      }
    >
      {isError ? (
        <EmptyState text="异常请求接口暂不可用。" />
      ) : events.length === 0 ? (
        <EmptyState text={isLoading ? '加载中…' : '暂无异常请求'} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>时间</TableHead>
              <TableHead>等级</TableHead>
              <TableHead>IP / 玩家名</TableHead>
              <TableHead>频道 / Key</TableHead>
              <TableHead>Endpoint</TableHead>
              <TableHead>规则 / 错误码</TableHead>
              <TableHead>处置</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {events.map((event) => <EventRow key={event.id} event={event} />)}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

function EventRow({ event }: { event: ClientDistSecurityEvent }) {
  const [searchParams] = useSearchParams()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const blockIP = useBlockClientDistIP()
  const canBlock = Boolean(event.ip)
  const monitorHref = buildClientDistHref('/client-dist-monitor', searchParams, {
    channelId: event.channelId,
    ip: event.ip,
    machineId: event.machineId,
    errCode: event.errCode,
    tab: 'logs',
  })
  return (
    <TableRow>
      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(event.createdAt)}</TableCell>
      <TableCell><Badge variant={levelVariant(event.severity)}>{event.severity}</Badge></TableCell>
      <TableCell>
        <div className="font-medium">{event.ip || EMPTY}</div>
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <span>{maskPlayerName(event.playerName) || EMPTY}</span>
          {event.playerName ? <UntrustedFieldBadge /> : null}
        </div>
      </TableCell>
      <TableCell>
        <div>{event.channelId || EMPTY}</div>
        <div className="text-xs text-muted-foreground">{event.keyId ?? EMPTY}</div>
      </TableCell>
      <TableCell className="max-w-44 truncate">{event.endpoint || EMPTY}</TableCell>
      <TableCell>
        <div>{event.ruleCode || EMPTY}</div>
        <div className="text-xs text-muted-foreground">{event.errCode || event.status || EMPTY}</div>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted-foreground">{event.action || EMPTY}</span>
          <Button asChild size="xs" variant="outline">
            <Link to={monitorHref}>查看分发日志</Link>
          </Button>
          {canBlock ? (
            <Button type="button" size="xs" variant="outline" className="text-status-danger" onClick={() => setConfirmOpen(true)}>
              封禁 IP
            </Button>
          ) : null}
        </div>
        <DangerConfirm
          open={confirmOpen}
          title={`临时封禁 IP ${event.ip}`}
          description="将对该 IP 施加临时封禁，确认后立即生效；可在「封禁与降级」中取消。"
          confirmLabel="确认封禁"
          pending={blockIP.isPending}
          onConfirm={() => {
            blockIP.mutate(
              { ip: event.ip, channelId: event.channelId || undefined, reason: event.reason || '事件行一键封禁', durationMinutes: 30 },
              {
                onSuccess: () => {
                  toast.success('已提交 IP 临时封禁')
                  setConfirmOpen(false)
                },
                onError: () => toast.error('IP 封禁失败'),
              },
            )
          }}
          onCancel={() => setConfirmOpen(false)}
        />
      </TableCell>
    </TableRow>
  )
}

function ProfilesTab() {
  const { query, updateQuery } = useSecurityQuery()
  const [playerName, setPlayerName] = useState('')
  const [detailId, setDetailId] = useState<number | null>(null)
  const { data, isError, isLoading } = useClientDistSecurityProfiles({
    playerName: playerName || undefined,
    channelId: query.channelId,
    machineId: query.machineId,
    ip: query.ip,
    limit: 200,
  })
  const profiles = data ?? []
  return (
    <div className="space-y-4">
      <TrustNotice />
      <Panel
        title="客户端画像"
        actions={
          <div className="flex flex-wrap gap-2">
            <Input className="w-40" placeholder="玩家名" value={playerName} onChange={(e) => setPlayerName(e.target.value)} />
            <Input className="w-40" placeholder="频道" value={query.channelId ?? ''} onChange={(e) => updateQuery({ channelId: e.target.value || null })} />
            <Input className="w-40" placeholder="Machine ID" value={query.machineId ?? ''} onChange={(e) => updateQuery({ machineId: e.target.value || null })} />
          </div>
        }
      >
        {isError ? (
          <EmptyState text="客户端画像接口暂不可用。" />
        ) : profiles.length === 0 ? (
          <EmptyState text={isLoading ? '加载中…' : '暂无客户端画像'} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>玩家 / 频道</TableHead>
                <TableHead>设备标识</TableHead>
                <TableHead>最近 IP / Key</TableHead>
                <TableHead>环境</TableHead>
                <TableHead>版本</TableHead>
                <TableHead>风险</TableHead>
                <TableHead>最近出现</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {profiles.map((profile) => (
                <TableRow key={profile.id}>
                  <TableCell>
                    <div className="flex items-center gap-1 font-medium" title={profile.playerName || undefined}>
                      <span>{maskPlayerName(profile.playerName) || EMPTY}</span>
                      {profile.playerName ? <UntrustedFieldBadge /> : null}
                    </div>
                    <div className="text-xs text-muted-foreground">{profile.channelId || EMPTY}</div>
                  </TableCell>
                  <TableCell className="max-w-56">
                    <div className="truncate text-xs font-mono" title={profile.machineId || undefined}>
                      machine: {maskMachineId(profile.machineId) || EMPTY}
                    </div>
                    <div className="truncate text-xs font-mono text-muted-foreground" title={profile.installId || undefined}>
                      install: {maskInstallId(profile.installId) || EMPTY}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div>{profile.lastIp || EMPTY}</div>
                    <div className="text-xs text-muted-foreground">{profile.keyPrefix || profile.keyId || EMPTY}</div>
                  </TableCell>
                  <TableCell>
                    <div>{profile.os || EMPTY} {profile.arch || ''}</div>
                    <div className="text-xs text-muted-foreground">{profile.javaVendor || EMPTY} {profile.javaVersion || ''}</div>
                  </TableCell>
                  <TableCell>
                    <div>core {profile.coreVersion || EMPTY}</div>
                    <div className="text-xs text-muted-foreground">manifest {profile.manifestVersion || EMPTY}</div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={levelVariant(profile.riskLevel)}>{profile.riskLevel || 'info'} · {profile.riskScore}</Badge>
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(profile.lastSeen)}</TableCell>
                  <TableCell className="text-right">
                    <Button type="button" size="xs" variant="outline" onClick={() => setDetailId(profile.id)}>
                      查看详情
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
      <ProfileDetailDialog id={detailId} open={detailId !== null} onOpenChange={(open) => !open && setDetailId(null)} />
    </div>
  )
}

function ProfileDetailDialog({ id, open, onOpenChange }: { id: number | null; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { data, isLoading, isError } = useClientDistSecurityProfile(id)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>客户端画像详情</DialogTitle>
          <DialogDescription>全量环境字段与风险时间线；玩家名 / 机器码不可信，仅供研判。</DialogDescription>
        </DialogHeader>
        {isError ? <EmptyState text="加载画像详情失败。" /> : null}
        {isLoading ? <EmptyState text="加载中…" /> : null}
        {data ? (
          <div className="space-y-4 text-sm">
            <div className="grid grid-cols-2 gap-2 rounded-lg border p-3 text-xs">
              <div>
                <div className="text-muted-foreground">玩家</div>
                <div className="flex items-center gap-1 font-medium">
                  <span>{maskPlayerName(data.playerName) || EMPTY}</span>
                  {data.playerName ? <UntrustedFieldBadge /> : null}
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">频道</div>
                <div>{data.channelId || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">机器码</div>
                <div className="font-mono" title={data.machineId || undefined}>{maskMachineId(data.machineId) || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">Install</div>
                <div className="font-mono" title={data.installId || undefined}>{maskInstallId(data.installId) || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">Java</div>
                <div>
                  <span>{data.javaVendor || EMPTY}</span>
                  {data.javaVersion ? <span className="ml-1">{data.javaVersion}</span> : null}
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">时区 / Locale</div>
                <div>
                  <span>{data.timezone || EMPTY}</span>
                  <span className="mx-1">·</span>
                  <span>{data.locale || EMPTY}</span>
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">Core / Wedge</div>
                <div>{data.coreVersion || EMPTY} / {data.wedgeVersion || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">内存档</div>
                <div>{data.memoryTier || EMPTY}</div>
              </div>
            </div>
            <div>
              <div className="mb-2 text-xs font-medium text-muted-foreground">风险时间线</div>
              <ul className="space-y-2">
                {(data.recentEvents ?? []).map((ev) => (
                  <li key={`ev-${ev.id}`} className="rounded border px-3 py-2 text-xs">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono">{ev.ruleCode || EMPTY}</span>
                      <Badge variant={levelVariant(ev.severity)}>{ev.severity}</Badge>
                    </div>
                    <div className="mt-1 text-muted-foreground">{ev.reason || EMPTY} · {fmtTime(ev.createdAt)}</div>
                  </li>
                ))}
                {(data.protectionActions ?? []).map((act) => (
                  <li key={`act-${act.id}`} className="rounded border px-3 py-2 text-xs">
                    <div className="font-mono">{act.action || EMPTY}</div>
                    <div className="mt-1 text-muted-foreground">{act.reason || EMPTY} · {fmtTime(act.createdAt)}</div>
                  </li>
                ))}
                {(data.recentEvents ?? []).length === 0 && (data.protectionActions ?? []).length === 0 ? (
                  <li className="text-xs text-muted-foreground">暂无时间线条目</li>
                ) : null}
              </ul>
            </div>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function IpAnalysisTab() {
  const { data, isError, isLoading } = useClientDistIpAnalysis({ limit: 200 })
  const rows = data ?? []
  return (
    <Panel title="IP 剖析">
      {isError ? (
        <EmptyState text="IP 剖析接口暂不可用。" />
      ) : rows.length === 0 ? (
        <EmptyState text={isLoading ? '加载中…' : '暂无 IP 风险聚合'} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>IP</TableHead>
              <TableHead>请求 / 拒绝</TableHead>
              <TableHead>无效 Key / 404 sha / Range</TableHead>
              <TableHead>下载量</TableHead>
              <TableHead>Key / 频道</TableHead>
              <TableHead>风险</TableHead>
              <TableHead>封禁</TableHead>
              <TableHead>最近出现</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.ip}>
                <TableCell className="font-medium">{row.ip}</TableCell>
                <TableCell>{row.requestCount} / {row.rejectCount}</TableCell>
                <TableCell>{row.invalidKeyCount} / {row.notFoundCount} / {row.rangeCount}</TableCell>
                <TableCell>{fmtBytes(row.downloadBytes)}</TableCell>
                <TableCell>{row.keyCount} / {row.channelCount}</TableCell>
                <TableCell>{row.riskScore}</TableCell>
                <TableCell><Badge variant={row.blocked ? 'destructive' : 'secondary'}>{row.blocked ? '已封禁' : '未封禁'}</Badge></TableCell>
                <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(row.lastSeen)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

function PlayerAnalysisTab() {
  const { data, isError, isLoading } = useClientDistPlayerAnalysis({ limit: 200 })
  const rows = data ?? []
  return (
    <div className="space-y-4">
      <TrustNotice />
      <Panel title="玩家名剖析">
        {isError ? (
          <EmptyState text="玩家名剖析接口暂不可用。" />
        ) : rows.length === 0 ? (
          <EmptyState text={isLoading ? '加载中…' : '暂无玩家名聚合'} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>玩家名</TableHead>
                <TableHead>Install / Machine</TableHead>
                <TableHead>IP / Key / 频道</TableHead>
                <TableHead>下载量</TableHead>
                <TableHead>异常请求</TableHead>
                <TableHead>风险</TableHead>
                <TableHead>最近出现</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={row.playerName}>
                  <TableCell className="font-medium">{row.playerName || EMPTY}</TableCell>
                  <TableCell>{row.installCount} / {row.machineCount}</TableCell>
                  <TableCell>{row.ipCount} / {row.keyCount} / {row.channelCount}</TableCell>
                  <TableCell>{fmtBytes(row.downloadBytes)}</TableCell>
                  <TableCell>{row.abnormalRequests}</TableCell>
                  <TableCell>{row.riskScore}</TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(row.lastSeen)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
    </div>
  )
}

function ActionsTab() {
  return (
    <div className="grid gap-4 xl:grid-cols-[360px_1fr]">
      <div className="space-y-4">
        <BlockIpForm />
        <KeyStateForm />
        <ChannelProtectionForm />
      </div>
      <ActionsTable />
    </div>
  )
}

function BlockIpForm() {
  const [ip, setIp] = useState('')
  const [durationMinutes, setDurationMinutes] = useState('30')
  const [reason, setReason] = useState('手动安全处置')
  const blockIP = useBlockClientDistIP()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    blockIP.mutate({ ip, reason, durationMinutes: Number(durationMinutes) }, {
      onSuccess: () => {
        toast.success('已提交 IP 临时封禁')
        setIp('')
      },
      onError: () => toast.error('IP 封禁失败'),
    })
  }
  return (
    <Panel title="手动封 IP" icon={<Ban className="size-4" />} tone="danger">
      <form className="space-y-3" onSubmit={submit}>
        <Input required placeholder="IP 地址" value={ip} onChange={(e) => setIp(e.target.value)} />
        <Input required min={1} type="number" placeholder="封禁分钟数" value={durationMinutes} onChange={(e) => setDurationMinutes(e.target.value)} />
        <Textarea required placeholder="原因" value={reason} onChange={(e) => setReason(e.target.value)} />
        <Button className="w-full" type="submit" disabled={blockIP.isPending}>提交封禁</Button>
      </form>
    </Panel>
  )
}

function KeyStateForm() {
  const [keyId, setKeyId] = useState('')
  const [state, setState] = useState<KeySecurityState>('observe')
  const [reason, setReason] = useState('手动调整 key 安全状态')
  const setKeyState = useSetClientDistKeyState()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setKeyState.mutate({ keyId, body: { state, reason } }, {
      onSuccess: () => toast.success('已提交 key 状态调整'),
      onError: () => toast.error('key 状态调整失败'),
    })
  }
  return (
    <Panel title="Key 状态调整" icon={<Gauge className="size-4" />}>
      <form className="space-y-3" onSubmit={submit}>
        <Input required placeholder="Key ID" value={keyId} onChange={(e) => setKeyId(e.target.value)} />
        <Select value={state} onValueChange={(v) => setState(v as KeySecurityState)}>
          <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="normal">正常</SelectItem>
            <SelectItem value="observe">观察</SelectItem>
            <SelectItem value="throttled">限速</SelectItem>
            <SelectItem value="suspended">暂停</SelectItem>
            <SelectItem value="revoked">吊销</SelectItem>
          </SelectContent>
        </Select>
        <Textarea required placeholder="原因" value={reason} onChange={(e) => setReason(e.target.value)} />
        <Button className="w-full" type="submit" disabled={setKeyState.isPending}>提交调整</Button>
      </form>
    </Panel>
  )
}

function ChannelProtectionForm() {
  const [channelId, setChannelId] = useState('')
  const [mode, setMode] = useState<ChannelProtectionMode>('retry_after')
  const [reason, setReason] = useState('手动开启频道保护')
  const [retryAfterSeconds, setRetryAfterSeconds] = useState('60')
  const setProtection = useSetClientDistChannelProtection()
  const clearProtection = useClearClientDistChannelProtection()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setProtection.mutate({ channelId, body: { mode, reason, retryAfterSeconds: Number(retryAfterSeconds) } }, {
      onSuccess: () => toast.success('已提交频道保护'),
      onError: () => toast.error('频道保护提交失败'),
    })
  }
  const clear = () => {
    if (!channelId) return
    clearProtection.mutate(channelId, {
      onSuccess: () => toast.success('已关闭频道保护'),
      onError: () => toast.error('关闭频道保护失败'),
    })
  }
  return (
    <Panel title="频道保护" icon={<RadioTower className="size-4" />} tone="warning">
      <form className="space-y-3" onSubmit={submit}>
        <Input required placeholder="频道 ID" value={channelId} onChange={(e) => setChannelId(e.target.value)} />
        <Select value={mode} onValueChange={(v) => setMode(v as ChannelProtectionMode)}>
          <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="throttle">降速</SelectItem>
            <SelectItem value="concurrency">降并发</SelectItem>
            <SelectItem value="queue">排队</SelectItem>
            <SelectItem value="retry_after">Retry-After</SelectItem>
          </SelectContent>
        </Select>
        <Input min={1} type="number" placeholder="Retry-After 秒" value={retryAfterSeconds} onChange={(e) => setRetryAfterSeconds(e.target.value)} />
        <Textarea required placeholder="原因" value={reason} onChange={(e) => setReason(e.target.value)} />
        <div className="grid grid-cols-2 gap-2">
          <Button type="submit" disabled={setProtection.isPending}>开启 / 调整</Button>
          <Button type="button" variant="outline" disabled={clearProtection.isPending || !channelId} onClick={clear}>关闭保护</Button>
        </div>
      </form>
    </Panel>
  )
}

function ActionsTable() {
  const { data, isError, isLoading } = useClientDistSecurityActions({ limit: 200 })
  const cancel = useCancelClientDistIPBlock()
  const actions = data ?? []
  const cancelAction = (action: ClientProtectionAction) => {
    cancel.mutate(action.id, {
      onSuccess: () => toast.success('已提交解封'),
      onError: () => toast.error('解封失败'),
    })
  }
  return (
    <Panel title="封禁与降级动作">
      {isError ? (
        <EmptyState text="处置动作接口暂不可用。" />
      ) : actions.length === 0 ? (
        <EmptyState text={isLoading ? '加载中…' : '暂无处置动作'} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>目标</TableHead>
              <TableHead>动作</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>原因</TableHead>
              <TableHead>到期</TableHead>
              <TableHead>来源</TableHead>
              <TableHead className="text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {actions.map((action) => (
              <TableRow key={action.id}>
                <TableCell>
                  <div className="font-medium">{action.targetValue}</div>
                  <div className="text-xs text-muted-foreground">{action.targetType}</div>
                </TableCell>
                <TableCell>{action.action}</TableCell>
                <TableCell><Badge variant={statusVariant(action.status)}>{action.status}</Badge></TableCell>
                <TableCell className="max-w-52 truncate">{action.reason || EMPTY}</TableCell>
                <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(action.expiresAt)}</TableCell>
                <TableCell>{action.auto ? '自动' : '手动'}</TableCell>
                <TableCell className="text-right">
                  {action.targetType === 'ip' && action.action === 'temp_block' && action.status === 'active' ? (
                    <Button size="xs" variant="outline" disabled={cancel.isPending} onClick={() => cancelAction(action)}>解封</Button>
                  ) : EMPTY}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

function GroupsTab() {
  const { data, isError, isLoading } = useClientSecurityGroups()
  const createGroup = useCreateClientSecurityGroup()
  const deleteGroup = useDeleteClientSecurityGroup()
  const [name, setName] = useState('')
  const [targetType, setTargetType] = useState<SecurityTargetType>('ip')
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; name: string } | null>(null)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    createGroup.mutate({ name, kind: 'manual', targetType, enabled: true, rule: null, actionPolicy: null }, {
      onSuccess: () => {
        toast.success('已创建安全分组')
        setName('')
      },
      onError: () => toast.error('创建安全分组失败'),
    })
  }
  const confirmDeleteGroup = () => {
    if (!deleteTarget) return
    deleteGroup.mutate(deleteTarget.id, {
      onSuccess: () => toast.success('已删除安全分组'),
      onError: () => toast.error('删除安全分组失败'),
      onSettled: () => setDeleteTarget(null),
    })
  }
  return (
    <>
    <div className="grid gap-4 xl:grid-cols-[320px_1fr]">
      <Panel title="新建手动分组" icon={<UsersRound className="size-4" />}>
        <form className="space-y-3" onSubmit={submit}>
          <Input required placeholder="分组名" value={name} onChange={(e) => setName(e.target.value)} />
          <Select value={targetType} onValueChange={(v) => setTargetType(v as SecurityTargetType)}>
            <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="ip">IP</SelectItem>
              <SelectItem value="key">Key</SelectItem>
              <SelectItem value="channel">频道</SelectItem>
              <SelectItem value="machine">Machine</SelectItem>
              <SelectItem value="install">Install</SelectItem>
              <SelectItem value="player">玩家名</SelectItem>
            </SelectContent>
          </Select>
          <Button className="w-full" type="submit" disabled={createGroup.isPending}>创建分组</Button>
        </form>
      </Panel>
      <Panel title="安全分组">
        {isError ? (
          <EmptyState text="安全分组接口暂不可用。" />
        ) : (data ?? []).length === 0 ? (
          <EmptyState text={isLoading ? '加载中…' : '暂无安全分组'} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>类型</TableHead>
                <TableHead>目标</TableHead>
                <TableHead>启用</TableHead>
                <TableHead>更新时间</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(data ?? []).map((group) => (
                <TableRow key={group.id}>
                  <TableCell className="font-medium">{group.name}</TableCell>
                  <TableCell>{group.kind}</TableCell>
                  <TableCell>{group.targetType}</TableCell>
                  <TableCell><Badge variant={group.enabled ? 'default' : 'outline'}>{group.enabled ? '启用' : '停用'}</Badge></TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(group.updatedAt)}</TableCell>
                  <TableCell className="text-right">
                    <Button size="xs" variant="outline" className="text-status-danger hover:text-status-danger" onClick={() => setDeleteTarget({ id: group.id, name: group.name })}>删除</Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
    </div>
      <DangerConfirm
        open={deleteTarget !== null}
        title={`删除安全分组「${deleteTarget?.name ?? ''}」`}
        description="删除后该分组的防护规则立即失效，此操作不可撤销。"
        pending={deleteGroup.isPending}
        onConfirm={confirmDeleteGroup}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  )
}

export default function ProtectionCenterPage() {
  const [searchParams] = useSearchParams()
  const [tab, setTab] = useTabParam('tab', 'overview', ['overview', 'events', 'logs', 'profiles', 'ip', 'actions', 'players', 'groups'])
  const monitorHref = buildClientDistHref('/client-dist-monitor', searchParams, { tab: 'logs' })
  const channelHref = buildClientDistHref('/client-channels', searchParams, { tab: 'stats' })
  return (
    <div data-page="client-dist-security" className="jm-page-stack space-y-4">
      <div className="jm-page-header">
        <div>
          <div className="flex items-center gap-2">
            <ShieldCheck className="size-5 text-primary" />
            <h1 className="jm-page-title">客户端分发安全</h1>
          </div>
          <p className="jm-page-subtitle">客户端分发安全总览、全量日志、画像剖析、封禁与降级管理。</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button asChild size="sm" variant="outline"><Link to={monitorHref}>打开分发监控</Link></Button>
          <Button asChild size="sm" variant="outline"><Link to={channelHref}>打开频道工作台</Link></Button>
          <Badge variant="outline"><ShieldAlert className="size-3" /> FR-264</Badge>
        </div>
      </div>
      <Tabs value={tab} onValueChange={setTab} className="space-y-4">
        <TabsList className="jm-toolbar-surface flex h-auto w-full flex-wrap justify-start gap-1 p-1">
          <TabsTrigger className="flex-none" value="overview">安全总览</TabsTrigger>
          <TabsTrigger className="flex-none" value="events">异常请求</TabsTrigger>
          <TabsTrigger className="flex-none" value="logs">日志详情</TabsTrigger>
          <TabsTrigger className="flex-none" value="profiles">客户端画像</TabsTrigger>
          <TabsTrigger className="flex-none" value="ip">IP 剖析</TabsTrigger>
          <TabsTrigger className="flex-none" value="actions">封禁与降级</TabsTrigger>
          <TabsTrigger className="flex-none" value="players">玩家名剖析</TabsTrigger>
          <TabsTrigger className="flex-none" value="groups">安全分组</TabsTrigger>
        </TabsList>
        <TabsContent value="overview"><OverviewTab /></TabsContent>
        <TabsContent value="events"><EventsTab /></TabsContent>
        <TabsContent value="logs"><LogsTab /></TabsContent>
        <TabsContent value="profiles"><ProfilesTab /></TabsContent>
        <TabsContent value="ip"><IpAnalysisTab /></TabsContent>
        <TabsContent value="actions"><ActionsTab /></TabsContent>
        <TabsContent value="players"><PlayerAnalysisTab /></TabsContent>
        <TabsContent value="groups"><GroupsTab /></TabsContent>
      </Tabs>
    </div>
  )
}
