import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useClientStats } from '@/api/clientStats'
import { useClientDistErrorSummary, useClientDistRealtime } from '@/api/clientDistEvents'
import { useClientRuntimeOverview } from '@/api/clientRuntimeStates'
import { useClientChannels } from '@/api/clientChannels'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'
import ClientDistExportButton from '@/components/ClientDistExportButton'
import { buildClientDistHref, readClientDistQuery, updateClientDistQuery } from '@/lib/client-dist-query'
import { ObsTimeRangePicker } from '@/components/client-dist/ObsTimeRangePicker'
import type { ObsWindow } from '@/components/client-dist/obs-window'
import { DEFAULT_SEG_BY_TAB, isOpsTab, normalizeOpsTab, resolveOpsSeg, type OpsSeg, type OpsTab } from '@/lib/client-dist-ops-tab'
import ClientDistLogsTab from '@/components/client-dist/ClientDistLogsTab'
import OpsOverviewTab from '@/components/client-dist/OpsOverviewTab'
import OpsStatisticsTab from '@/components/client-dist/OpsStatisticsTab'
import OpsRealtimeTab from '@/components/client-dist/OpsRealtimeTab'
import OpsClientsTab from '@/components/client-dist/OpsClientsTab'
import { EventsTab } from '@/components/client-dist/SecurityEventsTab'
import { ProfilesTab } from '@/components/client-dist/SecurityProfilesTab'
import { IpAnalysisTab, PlayerAnalysisTab } from '@/components/client-dist/SecurityAnalysisTabs'
import { ActionsTab } from '@/components/client-dist/SecurityActionsTab'
import { GroupsTab } from '@/components/client-dist/SecurityGroupsTab'
import { OPS_ALL_CHANNELS, rangeForSpan, toApiRange, toStatsDays, type RuntimeLink } from '@/components/client-dist/ops-shared'
import { type MetricRange } from '@jianmanager/ui'

const ROLE_PLATFORM_ADMIN = 10

/** Tab 内分档切换器（次要分段控件，写回 `seg`）。 */
function SegSwitcher({
  seg,
  options,
  onChange,
}: {
  seg: OpsSeg
  options: { value: OpsSeg; labelKey: string }[]
  onChange: (v: OpsSeg) => void
}) {
  const { t } = useTranslation()
  return (
    <Tabs value={seg} onValueChange={(v) => onChange(v as OpsSeg)}>
      <TabsList variant="line">
        {options.map((o) => (
          <TabsTrigger key={o.value} value={o.value}>{t(o.labelKey)}</TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}

/**
 * 页面 B「客户端分发运维」外壳（FR-430 / ADR-088）。
 * 7 Tab（总览/统计/实时监控/全量日志/机器·客户端/画像/处置）；旧安全页 8 Tab 收敛进其中 4 个（`seg` 分档）。
 * `data-page="client-dist-ops"`；查询级 `enabled: isPlatformAdmin` 纵深防御（路由守卫见 Workspace）。
 *
 * 安全侧 6 个 Tab 已抽为独立组件（`components/client-dist/Security*`），本文件仅保留外壳与分档切换。
 */
export default function ProtectionCenterPage() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const query = readClientDistQuery(searchParams)
  const isPlatformAdmin = useAuthStore((s) => s.role) === ROLE_PLATFORM_ADMIN

  const normalized = normalizeOpsTab(searchParams.get('tab'), 'security')
  const tab: OpsTab = normalized.tab
  const seg = resolveOpsSeg(searchParams.get('seg'), tab, normalized.seg)

  const setTab = (value: string) => {
    const next = isOpsTab(value) ? value : 'overview'
    setSearchParams(updateClientDistQuery(searchParams, { tab: next, seg: null }), { replace: true })
  }
  const setSeg = (value: OpsSeg) => {
    const fallback = DEFAULT_SEG_BY_TAB[tab]
    setSearchParams(updateClientDistQuery(searchParams, { seg: value === fallback ? null : value }), { replace: true })
  }

  // FR-425：时间窗 = URL from/to（自定义）优先，否则本地预设档。
  const [range, setRange] = useState<MetricRange>('7d')
  const customWindow: ObsWindow | null = query.from && query.to ? { from: query.from, to: query.to } : null
  const obsWindow: ObsWindow = customWindow ?? { range: toApiRange(range) }
  const effRange: MetricRange = customWindow ? (rangeForSpan(customWindow.from, customWindow.to) ?? range) : range
  const setTimeWindow = (w: ObsWindow) => {
    if ('range' in w) {
      setSearchParams(updateClientDistQuery(searchParams, { from: null, to: null }), { replace: true })
      setRange(w.range as MetricRange)
      return
    }
    setSearchParams(updateClientDistQuery(searchParams, { from: w.from, to: w.to }), { replace: true })
  }

  const { data: channels } = useClientChannels()
  const channelId = query.channelId
  const channel = channelId ?? OPS_ALL_CHANNELS

  const [runtimeLinkExtra, setRuntimeLinkExtra] = useState<RuntimeLink>({})
  const runtimeLink: RuntimeLink = {
    ...runtimeLinkExtra,
    machineId: query.machineId,
    version: query.version ? Number(query.version) : undefined,
    errCode: query.errCode,
    ip: query.ip,
  }
  const openLogsWithLink = (link: RuntimeLink) => {
    setRuntimeLinkExtra((prev) => ({ ...prev, ...link }))
    setSearchParams(updateClientDistQuery(searchParams, {
      machineId: link.machineId,
      version: link.version,
      errCode: link.errCode,
      ip: link.ip,
      tab: 'logs',
      type: 'request',
    }), { replace: true })
  }
  const clearRuntimeLink = () => {
    setRuntimeLinkExtra({})
    setSearchParams(updateClientDistQuery(searchParams, {
      ip: null,
      machineId: null,
      errCode: null,
      version: null,
    }), { replace: true })
  }

  const statsQuery = useClientStats(channelId, toStatsDays(effRange), { enabled: isPlatformAdmin && (tab === 'overview' || tab === 'statistics') })
  const realtimeQuery = useClientDistRealtime({ channelId, enabled: isPlatformAdmin && tab === 'monitor' })
  const runtimeQuery = useClientRuntimeOverview({
    channelId,
    range: toApiRange(effRange),
    enabled: isPlatformAdmin && (tab === 'overview' || tab === 'clients'),
  })
  const errorSummaryQuery = useClientDistErrorSummary({
    channelId,
    range: toApiRange(effRange),
    enabled: isPlatformAdmin && (tab === 'monitor' || tab === 'statistics'),
  })

  const channelHref = buildClientDistHref('/client-channels', searchParams, { tab: 'stats' })
  const channelPicker = (
    <Select
      value={channel}
      onValueChange={(value) => setSearchParams(updateClientDistQuery(searchParams, {
        channelId: value === OPS_ALL_CHANNELS ? null : value,
      }), { replace: true })}
    >
      <SelectTrigger size="sm" className="w-44"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value={OPS_ALL_CHANNELS}>{t('clientDistOps.allChannels')}</SelectItem>
        {(channels ?? []).map((c) => <SelectItem key={c.channelId} value={c.channelId}>{c.name}</SelectItem>)}
      </SelectContent>
    </Select>
  )

  return (
    <div data-page="client-dist-ops" className="jm-page-stack space-y-4">
      <div className="jm-page-header flex-wrap">
        <h1 className="jm-page-title">{t('nav.clientDistOps')}</h1>
        <div className="flex flex-wrap items-center gap-2">
          {isPlatformAdmin && channelPicker}
          {isPlatformAdmin && <ObsTimeRangePicker value={obsWindow} onChange={setTimeWindow} />}
          {isPlatformAdmin && <ClientDistExportButton kind="stats-summary" filters={{ channelId, range: toApiRange(effRange) }} />}
          <Button asChild size="sm" variant="outline"><Link to={channelHref}>{t('clientDistOps.openChannelWorkbench')}</Link></Button>
        </div>
      </div>

      {!isPlatformAdmin ? (
        <Panel>
          <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistOps.adminOnly')}</p>
        </Panel>
      ) : (
        <Tabs value={tab} onValueChange={setTab} className="space-y-4">
          <TabsList aria-label={t('clientDistOps.tabsLabel')} className="jm-toolbar-surface flex h-auto w-full flex-wrap justify-start gap-1 p-1">
            <TabsTrigger className="flex-none" value="overview">{t('clientDistOps.tabOverview')}</TabsTrigger>
            <TabsTrigger className="flex-none" value="statistics">{t('clientDistOps.tabStatistics')}</TabsTrigger>
            <TabsTrigger className="flex-none" value="monitor">{t('clientDistOps.tabMonitor')}</TabsTrigger>
            <TabsTrigger className="flex-none" value="logs">{t('clientDistOps.tabLogs')}</TabsTrigger>
            <TabsTrigger className="flex-none" value="clients">{t('clientDistOps.tabClients')}</TabsTrigger>
            <TabsTrigger className="flex-none" value="profiles">{t('clientDistOps.tabProfiles')}</TabsTrigger>
            <TabsTrigger className="flex-none" value="actions">{t('clientDistOps.tabActions')}</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="space-y-4">
            <OpsOverviewTab
              channelId={channelId}
              window={obsWindow}
              stats={statsQuery.data}
              runtime={runtimeQuery.data}
              onLink={openLogsWithLink}
            />
          </TabsContent>

          <TabsContent value="statistics" className="space-y-4">
            <OpsStatisticsTab stats={statsQuery.data} isError={statsQuery.isError} isLoading={statsQuery.isLoading} onLink={openLogsWithLink} />
          </TabsContent>

          <TabsContent value="monitor" className="space-y-4">
            <SegSwitcher
              seg={seg ?? 'live'}
              options={[{ value: 'live', labelKey: 'clientDistOps.segLive' }, { value: 'events', labelKey: 'clientDistOps.segEvents' }]}
              onChange={setSeg}
            />
            {seg === 'events' ? (
              <EventsTab />
            ) : (
              <OpsRealtimeTab realtime={realtimeQuery.data} errors={errorSummaryQuery.data} isError={realtimeQuery.isError || errorSummaryQuery.isError} onLink={openLogsWithLink} />
            )}
          </TabsContent>

          <TabsContent value="logs" className="space-y-4">
            <ClientDistLogsTab channelId={channelId} range={toApiRange(effRange)} enabled={isPlatformAdmin} link={runtimeLink} onClearLink={clearRuntimeLink} />
          </TabsContent>

          <TabsContent value="clients" className="space-y-4">
            <OpsClientsTab channelId={channelId} window={obsWindow} overview={runtimeQuery.data} isError={runtimeQuery.isError} onLink={openLogsWithLink} />
          </TabsContent>

          <TabsContent value="profiles" className="space-y-4">
            <SegSwitcher
              seg={seg ?? 'client'}
              options={[
                { value: 'client', labelKey: 'clientDistOps.segClient' },
                { value: 'ip', labelKey: 'clientDistOps.segIp' },
                { value: 'player', labelKey: 'clientDistOps.segPlayer' },
              ]}
              onChange={setSeg}
            />
            {seg === 'ip' ? <IpAnalysisTab /> : seg === 'player' ? <PlayerAnalysisTab /> : <ProfilesTab />}
          </TabsContent>

          <TabsContent value="actions" className="space-y-4">
            <div className="space-y-2">
              <SegSwitcher
                seg={seg ?? 'actions'}
                options={[
                  { value: 'actions', labelKey: 'clientDistOps.segActions' },
                  { value: 'groups', labelKey: 'clientDistOps.segGroups' },
                ]}
                onChange={setSeg}
              />
              <p className="text-xs text-muted-foreground">
                {(seg ?? 'actions') === 'groups'
                  ? t('clientDistOps.segGroupsDesc', '把要盯的 IP / 密钥 / 频道等打成一组，方便以后批量盯防和处置。')
                  : t('clientDistOps.segActionsDesc', '对可疑来源动手：临时封 IP、调低密钥限速、给频道开保护。下面表格是全部处置记录。')}
              </p>
            </div>
            {seg === 'groups' ? <GroupsTab /> : <ActionsTab />}
          </TabsContent>
        </Tabs>
      )}
    </div>
  )
}
