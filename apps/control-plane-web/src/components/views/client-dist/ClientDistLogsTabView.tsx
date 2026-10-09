/* eslint-disable react-refresh/only-export-components -- 受控视图与日志类型取值/标签助手同文件导出（仅影响 Fast Refresh） */
/**
 * @file ClientDistLogsTabView：页面 B · 全量日志 Tab 的受控视图
 *       （类型选择器 + 加厚请求表 + 安全聚合表 + 请求脱敏详情弹窗）。
 *       两路取数（请求事件检索 / 安全聚合日志）、详情取数、路由查询串读写（含 `type` 视图切换的
 *       解析与写回）与导出按钮接线均由应用容器负责；视图只保留展示、本地 UI 状态（详情弹窗开合）
 *       与受控上报。
 * @input lib/client-dist-events-contracts（ClientDistEvent/ClientDistEventDetail）、
 *        lib/client-dist-security-contracts（ClientDistSecurityLogItem/ClientDistSecurityLogType）、
 *        lib/client-dist-query（ClientDistQuery/ClientDistQueryKey）、
 *        views/client-dist/OpsShared（OPS_ALL/ResultBadge/fmtTime/kindLabel/targetOf/RuntimeLink）、
 *        Badge/Button/Dialog/Input/Panel/Select/Table 原语、翻译上下文
 * @output ClientDistLogTypeValue、CLIENT_DIST_LOG_TYPE_VALUES、isClientDistLogTypeValue、clientDistLogTypeLabel、
 *         ClientDistLogsLinkRenderer、ClientDistRequestLogsView（+ Props）、ClientDistAggregateLogsView（+ Props）
 * @sync apps/control-plane-web/src/components/client-dist/ClientDistLogsTab.tsx、
 *       apps/control-plane-web/src/components/client-dist/ClientDistLogsTab.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-430 / ADR-088 去重合并的唯一入口，含 FR-357 加厚请求表、FR-265 脱敏详情）
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import type { ClientDistEvent, ClientDistEventDetail } from '@jianmanager/ui/lib/client-dist-events-contracts'
import type { ClientDistQuery, ClientDistQueryKey } from '@/lib/client-dist-query'
import type { ClientDistSecurityLogItem, ClientDistSecurityLogType } from '@jianmanager/ui/lib/client-dist-security-contracts'
import { OPS_ALL, ResultBadge, fmtTime, kindLabel, targetOf, type RuntimeLink } from './OpsShared'

/** 全量日志视图取值集合（request 为独立加厚视图，其余走安全聚合流）。 */
export const CLIENT_DIST_LOG_TYPE_VALUES = ['all', 'hello', 'risk', 'action', 'request', 'runtime', 'telemetry'] as const

/** 全量日志视图取值（含聚合哨兵 `all`）。 */
export type ClientDistLogTypeValue = (typeof CLIENT_DIST_LOG_TYPE_VALUES)[number]

/** 路由参数 → 日志视图类型（非法值返回 false，缺省由容器回落到 `request`）。 */
export function isClientDistLogTypeValue(v: string | null): v is ClientDistLogTypeValue {
  return !!v && (CLIENT_DIST_LOG_TYPE_VALUES as readonly string[]).includes(v)
}

/** 日志类型 → 本地化标签（取值口径与安全侧契约 `ClientDistSecurityLogType | 'all'` 对齐）。 */
export function clientDistLogTypeLabel(value: ClientDistSecurityLogType | 'all', t: (key: string) => string): string {
  switch (value) {
    case 'all':
      return t('clientDistOps.logs.typeAll')
    case 'hello':
      return t('clientDistOps.logs.typeHello')
    case 'risk':
      return t('clientDistOps.logs.typeRisk')
    case 'action':
      return t('clientDistOps.logs.typeAction')
    case 'request':
      return t('clientDistOps.logs.typeRequest')
    case 'runtime':
      return t('clientDistOps.logs.typeRuntime')
    case 'telemetry':
      return t('clientDistOps.logs.typeTelemetry')
    default:
      return value
  }
}

/**
 * 深链渲染器：视图给出目标与样式类名，容器提供 router 语义（保持 SPA 导航）。
 * 缺省时视图退化为原生 `<a href>`，便于无路由环境（组件博物馆）独立渲染。
 */
export type ClientDistLogsLinkRenderer = (args: { to: string; className: string; children: ReactNode }) => ReactNode

/** 全量日志类型选择器（视图切换：request=加厚请求日志；其余=安全聚合 6 类型）。 */
function LogTypeSelect({ value, onChange }: { value: ClientDistLogTypeValue; onChange: (v: ClientDistLogTypeValue) => void }) {
  const { t } = useTranslation()
  return (
    <Select value={value} onValueChange={(v) => onChange(v as ClientDistLogTypeValue)}>
      <SelectTrigger size="sm" className="w-36"><SelectValue /></SelectTrigger>
      <SelectContent>
        {CLIENT_DIST_LOG_TYPE_VALUES.map((key) => (
          <SelectItem key={key} value={key}>{clientDistLogTypeLabel(key, t)}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/**
 * 加厚请求日志视图的注入契约（受控视图，ADR-097 a 范式）。
 *
 * 受控边界：请求事件行与加载/错误态、详情数据经 props 注入（容器调
 * `useClientDistEventSearch` / `useClientDistEventDetail`）；会触发重新取数的筛选
 * （`outcome`/`kind`）与来自路由的 `type` 经 props 受控，变更经 `onOutcomeChange` /
 * `onKindChange` / `onTypeChange` 上报。
 * 详情弹窗开合（当前查看的事件 id）是纯 UI 状态，留在视图内，仅在上报时告知容器以驱动取详情。
 * 注入契约：导出按钮经 `exportSlot` 注入（应用侧接线层，包内不引用）；联动条两个页内深链目标
 * 由容器按当前查询串构造，锚点经 `renderLink` 注入。
 */
export interface ClientDistRequestLogsViewProps {
  /** 当前日志视图类型（URL `type` 解析结果，缺省 request）。 */
  type: ClientDistLogTypeValue
  /** 类型切换上报（容器写回查询串，会切换视图并重新取数）。 */
  onTypeChange: (value: ClientDistLogTypeValue) => void
  /** 运行态联动过滤上下文（页面持有）。 */
  link: RuntimeLink
  /** 清除联动过滤。 */
  onClearLink: () => void
  /** 请求事件行（空数组即空态）。 */
  events: ClientDistEvent[]
  /** 取数中；仅在无行时展示「加载中」文案。 */
  eventsLoading: boolean
  /** 取数失败；判定优先于空态。 */
  eventsError: boolean
  /** 结果筛选（触发重新取数，故归容器）；`OPS_ALL` 表示不筛选。 */
  outcome: string
  /** 结果筛选变更上报。 */
  onOutcomeChange: (value: string) => void
  /** 事件种类筛选（触发重新取数，故归容器）；`OPS_ALL` 表示不筛选。 */
  kind: string
  /** 事件种类筛选变更上报。 */
  onKindChange: (value: string) => void
  /** 详情数据（容器按上报的 id 取数）；未选或未就绪时缺省。 */
  detail?: ClientDistEventDetail
  /** 详情取数中。 */
  detailLoading: boolean
  /** 详情取数失败。 */
  detailError: boolean
  /** 详情弹窗目标变更上报（`null` = 已关闭）；容器据此决定是否取详情。 */
  onDetailChange: (id: number | null) => void
  /** 「打开安全中心」深链目标（容器按当前查询串构造）。 */
  securityCenterHref: string
  /** 「打开频道工作台」深链目标（容器按当前查询串构造）。 */
  channelWorkbenchHref: string
  /** 深链渲染（容器注入 router Link）；缺省退化为原生 `<a href>`。 */
  renderLink?: ClientDistLogsLinkRenderer
  /** 导出按钮插槽（应用侧接线层，视图只决定渲染位置）。 */
  exportSlot?: ReactNode
}

/** 加厚请求日志视图（迁自旧监控页 `LogsTab`）。 */
export function ClientDistRequestLogsView({
  type,
  onTypeChange,
  link,
  onClearLink,
  events,
  eventsLoading,
  eventsError,
  outcome,
  onOutcomeChange,
  kind,
  onKindChange,
  detail,
  detailLoading,
  detailError,
  onDetailChange,
  securityCenterHref,
  channelWorkbenchHref,
  renderLink,
  exportSlot,
}: ClientDistRequestLogsViewProps) {
  const { t } = useTranslation()
  const [detailId, setDetailId] = useState<number | null>(null)

  // 开合状态留在视图（纯 UI 状态），同时向容器上报目标 id 以驱动详情取数。
  const changeDetail = (id: number | null) => {
    setDetailId(id)
    onDetailChange(id)
  }

  const hasLink = Object.values(link).some((v) => v !== undefined && v !== '')

  const filters = (
    <div className="flex flex-wrap items-center gap-2">
      <LogTypeSelect value={type} onChange={onTypeChange} />
      <Select value={outcome} onValueChange={onOutcomeChange}>
        <SelectTrigger size="sm" className="w-32"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={OPS_ALL}>{t('clientDistOps.outcomeAll')}</SelectItem>
          <SelectItem value="success">{t('clientDistOps.outcomeSuccess')}</SelectItem>
          <SelectItem value="failure">{t('clientDistOps.outcomeFailure')}</SelectItem>
        </SelectContent>
      </Select>
      <Select value={kind} onValueChange={onKindChange}>
        <SelectTrigger size="sm" className="w-32"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={OPS_ALL}>{t('clientDistOps.allKinds')}</SelectItem>
          <SelectItem value="manifest">{t('clientDistOps.kindManifest')}</SelectItem>
          <SelectItem value="artifact">{t('clientDistOps.kindArtifact')}</SelectItem>
        </SelectContent>
      </Select>
      {hasLink && <Button type="button" variant="outline" size="sm" onClick={onClearLink}>{t('clientDistOps.clearLink')}</Button>}
      {exportSlot}
    </div>
  )

  return (
    <>
      <Panel title={t('clientDistOps.logsTitle')} actions={filters}>
        {hasLink && (
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-lg border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
            <span>{t('clientDistOps.linkedFilter')}</span>
            {link.machineId && <Badge variant="outline" className="font-mono">machine={link.machineId}</Badge>}
            {link.runtimeVersion !== undefined && <Badge variant="outline">version=v{link.runtimeVersion}</Badge>}
            {link.version !== undefined && <Badge variant="outline">version=v{link.version}</Badge>}
            {link.coreVersion && <Badge variant="outline">core={link.coreVersion}</Badge>}
            {link.platform && <Badge variant="outline">platform={link.platform}</Badge>}
            {link.lag !== undefined && <Badge variant="outline">lag={link.lag}</Badge>}
            {link.errCode && <Badge variant="outline" className="font-mono">errCode={link.errCode}</Badge>}
            {link.ip && <Badge variant="outline" className="font-mono">ip={link.ip}</Badge>}
            <HintLink to={securityCenterHref} className="font-medium text-primary hover:underline" renderLink={renderLink}>
              {t('clientDistOps.logs.openSecurityCenter')}
            </HintLink>
            <HintLink to={channelWorkbenchHref} className="font-medium text-primary hover:underline" renderLink={renderLink}>
              {t('clientDistOps.logs.openChannelWorkbench')}
            </HintLink>
          </div>
        )}
        {eventsError ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistOps.eventsError')}</p>
        ) : events.length === 0 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{eventsLoading ? t('common.loading') : t('clientDistOps.eventsEmpty')}</p>
        ) : (
          <EventTable events={events} onDetail={changeDetail} />
        )}
      </Panel>
      <EventDetailDialog
        open={detailId !== null}
        onOpenChange={(open) => !open && changeDetail(null)}
        detail={detail}
        isLoading={detailLoading}
        isError={detailError}
      />
    </>
  )
}

/**
 * 安全聚合多类型视图的注入契约（受控视图，ADR-097 a 范式）。
 *
 * 受控边界：聚合日志行与加载/错误态经 props 注入（容器调 `useClientDistSecurityLogs`）；
 * 会触发重新取数的查询条件（`query` 来自路由查询串、`playerName` 为筛选）经 props 受控，
 * 变更经 `onQueryChange` / `onPlayerNameChange` 上报，容器负责写回查询串。
 */
export interface ClientDistAggregateLogsViewProps {
  /** 当前日志视图类型（`request` 不走本视图，由容器分流）。 */
  type: ClientDistLogTypeValue
  /** 类型切换上报（容器写回查询串，会切换视图并重新取数）。 */
  onTypeChange: (value: ClientDistLogTypeValue) => void
  /** 冻结查询条件（channelId/machineId/ip/errCode，来自路由查询串）。 */
  query: ClientDistQuery
  /** 查询条件写入（容器接 router 查询串，会触发重新取数）。 */
  onQueryChange: (patch: Partial<Record<ClientDistQueryKey, string | null>>) => void
  /** 玩家名筛选（触发重新取数，故归容器）。 */
  playerName: string
  /** 玩家名筛选变更上报。 */
  onPlayerNameChange: (value: string) => void
  /** 聚合日志行（空数组即空态）。 */
  logs: ClientDistSecurityLogItem[]
  /** 取数中；仅在无行时展示「加载中」文案。 */
  logsLoading: boolean
  /** 取数失败；判定优先于空态。 */
  logsError: boolean
  /** 导出按钮插槽（应用侧接线层，视图只决定渲染位置）。 */
  exportSlot?: ReactNode
}

/** 安全聚合多类型视图（迁自安全页 `LogsTab`）。 */
export function ClientDistAggregateLogsView({
  type,
  onTypeChange,
  query,
  onQueryChange,
  playerName,
  onPlayerNameChange,
  logs,
  logsLoading,
  logsError,
  exportSlot,
}: ClientDistAggregateLogsViewProps) {
  const { t } = useTranslation()

  const filters = (
    <div className="flex flex-wrap gap-2">
      <LogTypeSelect value={type} onChange={onTypeChange} />
      <Input className="w-36" placeholder={t('clientDistOps.logs.phChannel')} value={query.channelId ?? ''} onChange={(e) => onQueryChange({ channelId: e.target.value || null })} />
      <Input className="w-40" placeholder={t('clientDistOps.logs.phMachine')} value={query.machineId ?? ''} onChange={(e) => onQueryChange({ machineId: e.target.value || null })} />
      <Input className="w-32" placeholder={t('clientDistOps.logs.phPlayer')} value={playerName} onChange={(e) => onPlayerNameChange(e.target.value)} />
      <Input className="w-36" placeholder={t('clientDistOps.logs.phIp')} value={query.ip ?? ''} onChange={(e) => onQueryChange({ ip: e.target.value || null })} />
      <Input className="w-40" placeholder={t('clientDistOps.logs.phErrCode')} value={query.errCode ?? ''} onChange={(e) => onQueryChange({ errCode: e.target.value || null })} />
      {exportSlot}
    </div>
  )

  return (
    <Panel title={t('clientDistOps.logs.title')} actions={filters}>
      {logsError ? (
        <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistOps.logs.error')}</p>
      ) : logs.length === 0 ? (
        <p className="py-10 text-center text-sm text-muted-foreground">{logsLoading ? t('common.loading') : t('clientDistOps.logs.empty')}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistOps.logs.colTime')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colType')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colTitle')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colObject')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colStatusErr')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colDetail')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {logs.map((item) => (
              <TableRow key={item.id}>
                <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(item.createdAt)}</TableCell>
                <TableCell><Badge variant="outline">{clientDistLogTypeLabel(item.type, t)}</Badge></TableCell>
                <TableCell className="font-medium">{item.title || '—'}</TableCell>
                <TableCell className="max-w-64 text-xs">
                  <div className="truncate">{t('clientDistOps.logs.prefixChannel')}{item.channelId || '—'}</div>
                  <div className="truncate text-muted-foreground">{t('clientDistOps.logs.prefixPlayer')}{item.playerName || '—'} · {t('clientDistOps.logs.prefixIp')}{item.ip || '—'}</div>
                  <div className="truncate text-muted-foreground">{t('clientDistOps.logs.prefixMachine')}{item.machineId || '—'}</div>
                </TableCell>
                <TableCell>
                  <div>{item.status || '—'}</div>
                  <div className="text-xs text-muted-foreground">{item.errCode || '—'}</div>
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

/** 联动条页内深链：容器注入了 router 渲染器则用其渲染，否则退化为原生锚点。 */
function HintLink({
  to,
  className,
  renderLink,
  children,
}: {
  to: string
  className: string
  renderLink?: ClientDistLogsLinkRenderer
  children: ReactNode
}) {
  if (renderLink) return <>{renderLink({ to, className, children })}</>
  return <a className={className} href={to}>{children}</a>
}

/** 请求日志表（加厚列，FR-357）。 */
function EventTable({ events, onDetail }: { events: ClientDistEvent[]; onDetail: (id: number) => void }) {
  const { t } = useTranslation()
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('clientDistOps.colTime')}</TableHead>
          <TableHead>{t('clientDistOps.colChannel')}</TableHead>
          <TableHead>{t('clientDistOps.colPlayer', '玩家名')}</TableHead>
          <TableHead>{t('clientDistOps.colMachine')}</TableHead>
          <TableHead>{t('clientDistOps.colCoreVersion', 'Core 版本')}</TableHead>
          <TableHead>{t('clientDistOps.colKind')}</TableHead>
          <TableHead>{t('clientDistOps.colTarget')}</TableHead>
          <TableHead>{t('clientDistOps.colIp')}</TableHead>
          <TableHead>{t('clientDistOps.colBytes', '字节')}</TableHead>
          <TableHead>{t('clientDistOps.colDuration', '耗时')}</TableHead>
          <TableHead>{t('clientDistOps.colStatus')}</TableHead>
          <TableHead>{t('clientDistOps.colResult')}</TableHead>
          <TableHead>{t('clientDistOps.colErrCode')}</TableHead>
          <TableHead>{t('clientDistOps.colDetail')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((e) => (
          <TableRow key={e.id}>
            <TableCell className="tabular-nums text-muted-foreground">{fmtTime(e.createdAt)}</TableCell>
            <TableCell>{e.channelId || '—'}</TableCell>
            <TableCell className="text-xs">{e.playerName || '—'}</TableCell>
            <TableCell className="font-mono text-xs">{e.machineId || '—'}</TableCell>
            <TableCell className="font-mono text-xs">{e.coreVersion || '—'}</TableCell>
            <TableCell>{kindLabel(e.kind, t)}</TableCell>
            <TableCell className="font-mono text-xs">{targetOf(e)}</TableCell>
            <TableCell className="tabular-nums">{e.ip || '—'}</TableCell>
            <TableCell className="tabular-nums">{e.bytes}</TableCell>
            <TableCell className="tabular-nums">{e.durationMs}ms</TableCell>
            <TableCell className="tabular-nums">{e.status}</TableCell>
            <TableCell><ResultBadge status={e.status} /></TableCell>
            <TableCell>{e.errCode ? <Badge variant="outline" className="font-mono text-xs">{e.errCode}</Badge> : <span className="text-muted-foreground">—</span>}</TableCell>
            <TableCell><Button type="button" variant="outline" size="xs" onClick={() => onDetail(e.id)}>{t('clientDistOps.viewDetail')}</Button></TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

/** 单条请求脱敏详情弹窗（FR-265）；数据与加载/错误态由容器按视图上报的 id 注入。 */
function EventDetailDialog({
  open,
  onOpenChange,
  detail,
  isLoading,
  isError,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  detail?: ClientDistEventDetail
  isLoading: boolean
  isError: boolean
}) {
  const { t } = useTranslation()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('clientDistOps.detailTitle')}</DialogTitle>
          <DialogDescription>{t('clientDistOps.detailHint')}</DialogDescription>
        </DialogHeader>
        {isError ? <p className="text-sm text-muted-foreground">{t('clientDistOps.detailError')}</p> : null}
        {isLoading ? <p className="text-sm text-muted-foreground">{t('common.loading')}</p> : detail ? <EventDetailBody detail={detail} /> : null}
      </DialogContent>
    </Dialog>
  )
}

function EventDetailBody({ detail }: { detail: ClientDistEventDetail }) {
  const { t } = useTranslation()
  const failed = detail.status >= 400 || !!detail.errCode
  const fmtDur = (ms?: number) => (!ms && ms !== 0 ? '—' : ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`)
  return (
    <div className="space-y-4 text-sm">
      <div className={`rounded-lg border p-3 ${failed ? 'border-destructive/40 bg-destructive/5' : 'border-emerald-500/40 bg-emerald-500/5'}`}>
        <div className="flex items-center gap-2">
          <Badge variant={failed ? 'destructive' : 'default'}>{detail.status}</Badge>
          <span className="font-medium">{detail.kind}</span>
          {detail.errCode ? <Badge variant="outline" className="font-mono">{detail.errCode}</Badge> : null}
          <span className="ml-auto text-xs text-muted-foreground">{fmtTime(detail.createdAt)}</span>
        </div>
        {detail.errReason ? <p className="mt-2 text-xs text-muted-foreground">{detail.errReason}</p> : null}
      </div>

      <section>
        <h4 className="mb-2 text-xs font-semibold">{t('clientDistOps.detailIdentity', '身份与来源')}</h4>
        <div className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-lg border p-3 text-xs sm:grid-cols-3">
          <DetailLine label={t('clientDistOps.colChannel')} value={detail.channelId || '—'} />
          <DetailLine label={t('clientDistOps.colIp')} value={detail.ip || '—'} mono />
          <DetailLine label={t('clientDistOps.colMachine', '机器 ID')} value={detail.machineId || '—'} mono breakAll />
          <DetailLine label={t('clientDistOps.colPlayer', '玩家')} value={detail.playerName || '—'} />
          <DetailLine label={t('clientDistOps.colCore', 'Core 版本')} value={detail.coreVersion || '—'} mono />
          <DetailLine label={t('clientDistOps.colDuration', '耗时')} value={fmtDur(detail.durationMs)} />
          <DetailLine label={t('clientDistOps.colVersion', '版本')} value={detail.version ? `v${detail.version}` : '—'} />
          <DetailLine label={t('clientDistOps.colBytes', '字节')} value={String(detail.bytes ?? 0)} />
          <DetailLine label={t('clientDistOps.colArtifact', '制品')} value={detail.artifactSha ? `${detail.artifactSha.slice(0, 12)}…` : '—'} mono />
        </div>
      </section>

      <section>
        <h4 className="mb-2 text-xs font-semibold">{t('clientDistOps.requestSection', '请求')}</h4>
        <div className="mb-2 grid grid-cols-[4rem_1fr] gap-2 rounded-lg border p-3 text-xs">
          <span className="font-mono text-muted-foreground">{detail.method || 'GET'}</span>
          <span className="break-all font-mono">{detail.path || '—'}</span>
        </div>
        <HeaderList title={t('clientDistOps.requestHeaders')} rows={detail.requestHeaders} />
      </section>

      <section>
        <h4 className="mb-2 text-xs font-semibold">{t('clientDistOps.responseSection', '响应')}</h4>
        <HeaderList title={t('clientDistOps.responseHeaders')} rows={detail.responseHeaders} />
        <div className="mt-2">
          <BodyBlock title={t('clientDistOps.responseBody', '响应体')} body={detail.responseBody} />
        </div>
        {detail.etag ? (
          <p className="mt-2 text-xs text-muted-foreground">
            ETag: <span className="break-all font-mono">{detail.etag}</span>
          </p>
        ) : null}
      </section>
    </div>
  )
}

function DetailLine({ label, value, mono, breakAll }: { label: string; value: string; mono?: boolean; breakAll?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground">{label}</div>
      <div className={`${mono ? 'font-mono' : ''} ${breakAll ? 'break-all' : 'break-words'}`}>{value}</div>
    </div>
  )
}

function BodyBlock({ title, body }: { title: string; body?: string }) {
  return (
    <div>
      <h4 className="mb-2 text-xs font-semibold">{title}</h4>
      {body ? (
        <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-lg border p-3 text-xs font-mono">{body}</pre>
      ) : (
        <p className="rounded-lg border p-3 text-xs text-muted-foreground">—</p>
      )}
    </div>
  )
}

function HeaderList({ title, rows }: { title: string; rows: Record<string, string> }) {
  const entries = Object.entries(rows ?? {})
  return (
    <div>
      <h4 className="mb-2 text-xs font-semibold">{title}</h4>
      {entries.length === 0 ? (
        <p className="rounded-lg border p-3 text-xs text-muted-foreground">—</p>
      ) : (
        <div className="overflow-hidden rounded-lg border">
          {entries.map(([k, v]) => (
            <div key={k} className="grid grid-cols-[11rem_1fr] border-b px-3 py-2 text-xs last:border-b-0">
              <span className="font-mono text-muted-foreground">{k}</span>
              <span className="min-w-0 truncate font-mono">{v}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
