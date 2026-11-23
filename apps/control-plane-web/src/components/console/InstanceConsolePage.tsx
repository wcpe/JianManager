// 展示主体已回迁应用侧（迁包已撤销）：页头（状态/标题/操作/指标条）、页签栏与概览面板都在包内；
// 本层保留路由参数解析、取数分发、keep-alive 生命周期宿主与各页签子组件的接线。
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Card } from '@jianmanager/ui/components/card'
import { Button } from '@jianmanager/ui/components/button'

import { useInstance, useKillInstance, useRebuildInstance, useRestartInstance, useStartInstance, useStopInstance, isProvisioningInstance, isRebuildingInstance } from '@/api/instances'
import { usePermissionsStore } from '@/stores/permissions'
import { runtimeDriftOf } from '@/lib/runtime-assets/runtime-drift'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { useInstanceMetrics, useMetricSeries } from '@/api/metrics'
import { useLogsFederation } from '@/api/logFederation'
import type { LogEntry } from '@/api/logs'
import { useNodes } from '@/api/nodes'
import { useServerState } from '@/api/serverState'
import { buildWatchItems } from '@/components/views/console/console-kpi-parts'
import { InstanceConsoleOverviewPanel, InstanceConsolePageView } from '@/components/views/console/InstanceConsolePageView'
import type { InstanceConsoleLinkArgs } from '@/components/views/console/InstanceConsolePageView'
import { TAB_CARD_TYPE, TAB_KEYS, readActiveTab, readResourceSegment, visibleTabsFor } from '@/lib/instances/instance-console-tabs'
import type { TabKey } from '@/lib/instances/instance-console-tabs'
import { useInstanceCapabilities, hasCapability } from '@/lib/instances/capabilities'
import InstanceActivityFeed from './InstanceActivityFeed'
import InstanceBackupSegment from './InstanceBackupSegment'
import InstancePlayersSegment from './InstancePlayersSegment'
import InstanceResourceSegment, { type ResourceSegment } from './InstanceResourceSegment'
import BcSegment from './BcSegment'
import BcPlayersPanel from './BcPlayersPanel'
import BinarySegment from './BinarySegment'
import BinaryVersionPanel from './BinaryVersionPanel'
import QuotaPanel from './QuotaPanel'
import SnapshotPanel from './SnapshotPanel'
import GenericConfigSegment from './GenericConfigSegment'
import { HealthPanel } from './HealthPanel'
import { RuntimeDriftBanner } from './RuntimeDriftNotice'
import WorkspaceCardBody from './WorkspaceCardBody'
import { recordRecentServer } from '@/lib/console/server-selection'

interface InstanceConsolePageProps {
  instanceId: number
}

/** 
 * 服务器统一控制台的容器：固定分区的单服默认入口。
 *
 * 展示主体已回迁应用侧：页头、页签栏与概览面板由 {@link InstanceConsolePageView} 渲染，
 * 页签内容经 `renderTabPanel` 按页签注入本层的接线组件。留在本层的是：
 * - 深链解析与写回（`?tab=` / `?seg=`）：「当前页签」会改变子组件取数（隐藏页签停轮询），属取数口径；
 * - 十余个取数 hook 的分发、能力画像门控（448）与「最近打开」记录；
 * - keep-alive 生命周期宿主：`mountedTabs` 决定哪些页签常驻挂载——
 *   访问过的页签进入集合全部渲染，非活跃者包 `<Activity mode="hidden">`——DOM 与本地状态保留、
 *   effects 卸载（TanStack Query 订阅随之暂停 → 隐藏页签自动停轮询），切回瞬时呈现；
 * - 各页签与横幅的接线：概览（配额/二进制版本/动态与告警）、文件配置、玩家/跨服、备份、
 *   BC 拓扑、进程、健康、配置、卡片型页签，以及运行态漂移横幅与强杀确认框。
 */
export default function InstanceConsolePage({ instanceId }: InstanceConsolePageProps) {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const resourceSegment = readResourceSegment(searchParams)
  const { data: instance, isLoading: instanceLoading, isError: instanceError, refetch: refetchInstance } = useInstance(instanceId)
  // 能力画像：Tab 显隐与顺序的唯一门控来源——后端下发的 capabilities 优先，
  // 缺失时按 (type, role) 本地兜底（离线/mock 兼容）。不再有零散 role === '...' 分支。
  const profile = useInstanceCapabilities(instance)
  const visibleTabs = useMemo(
    () => (instance ? visibleTabsFor(profile.capabilities) : TAB_KEYS),
    [instance, profile],
  )
  const activeTabFromUrl = readActiveTab(searchParams)
  // 深链落在隐藏 Tab：回退 overview，不白屏。
  const activeTab: TabKey = visibleTabs.includes(activeTabFromUrl) ? activeTabFromUrl : 'overview'
  // 访问过即保活：渲染期把新激活页签并入集合（React 官方「渲染期间调整状态」模式）。
  const [mountedTabs, setMountedTabs] = useState<TabKey[]>([activeTab])
  if (!mountedTabs.includes(activeTab)) {
    setMountedTabs((prev) => (prev.includes(activeTab) ? prev : [...prev, activeTab]))
  }
  const { data: nodes = [] } = useNodes({ refetchInterval: 30_000 })
  const { data: metrics } = useInstanceMetrics(instanceId, true)
  const { data: serverState } = useServerState(instanceId, true, 15_000)
  // 概览最近日志快照：复用 VL 联邦查询；只在概览可见时刷新，不建立尾随通道。
  // 原因：CP `logs` 表正在废弃（正文与业务数据混库、关键词检索全表扫），
  // 而实例日志的权威副本本就在 Worker 的 VL 里。
  // 注意：实例停机不影响本查询——Worker 仍在跑、VL 数据仍在。
  const [overviewLogTime, setOverviewLogTime] = useState(() => Date.now())
  useEffect(() => {
    if (activeTab !== 'overview') return
    const timer = window.setInterval(() => setOverviewLogTime(Date.now()), 10_000)
    return () => window.clearInterval(timer)
  }, [activeTab])
  const overviewLogWindow = useMemo(() => ({
    instanceId,
    limit: 40,
    from: new Date(overviewLogTime - 3_600_000).toISOString(),
    to: new Date(overviewLogTime).toISOString(),
  }), [instanceId, overviewLogTime])
  const { data: logFederation, isLoading: logsLoading, isError: logsError } = useLogsFederation(
    overviewLogWindow,
    { enabled: activeTab === 'overview', refetchInterval: false },
  )
  const logs = useMemo<LogEntry[]>(() => {
    const items = logFederation?.response.items
    if (!Array.isArray(items)) return []
    return items.map((raw, index) => {
      const o = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
      return {
        id: typeof o.id === 'number' ? o.id : -(index + 1),
        source: 'instance',
        level: typeof o.level === 'string' ? o.level.toLowerCase() : 'info',
        instanceId,
        instanceUuid: instance?.uuid ?? '',
        nodeId: 0,
        message: typeof o.message === 'string' ? o.message : typeof o._msg === 'string' ? o._msg : '',
        time: typeof o.event_time_utc === 'string' ? o.event_time_utc : '',
      } satisfies LogEntry
    })
  }, [logFederation, instanceId, instance?.uuid])

  const restart = useRestartInstance()
  const stop = useStopInstance()
  const start = useStartInstance()
  const kill = useKillInstance()
  const rebuild = useRebuildInstance()
  // 首批写门禁：无 instance.operate 时主操作按钮禁用（平台管理员 hasPerm 恒 true）。
  const canOperate = usePermissionsStore((s) => s.hasPerm('instance.operate'))
  // 强杀走统一危险操作确认，不直发请求。
  const [killConfirmOpen, setKillConfirmOpen] = useState(false)
  // 启/停/重启也要二次确认：单次误点会直接踢掉在线玩家并停服，而列表页已有同样口径的
  // 确认流程（见 InstancesPage 的 DangerConfirm）。详情页原先直接 mutate，安全语义不一致。
  const [lifecycleConfirm, setLifecycleConfirm] = useState<'start' | 'stop' | 'restart' | null>(null)

  // 直接经路由/深链进入实例也计入「最近打开」（与选择器/侧栏常驻列同一存储）；
  // store 侧对内容未变的写入不广播，轮询刷新不会造成订阅方空转。
  useEffect(() => {
    if (instance) recordRecentServer(instance)
  }, [instance])

  const node = nodes.find((n) => n.id === instance?.nodeId)
  const serverStatePlayers = serverState?.state?.server?.onlinePlayers
  const serverStateMax = serverState?.state?.server?.maxPlayers
  const playersAvailable = serverStatePlayers != null || (metrics?.playersAvailable ?? false)
  const online = serverStatePlayers ?? (metrics?.playersAvailable ? metrics!.onlinePlayers : 0)
  const maxPlayers = serverStateMax ?? (metrics?.maxPlayersAvailable ? metrics!.maxPlayers : 0)
  const maxPlayersAvailable = serverStateMax != null || (metrics?.maxPlayersAvailable ?? false)
  // 关注事项走 i18n（修硬编码中文）：告警文案直接进英文界面是验收硬伤。
  const watchItems = useMemo(
    () => buildWatchItems({ status: instance?.status, metrics, probeConnected: serverState?.connected, mcSemantics: profile.mcSemantics, t }),
    [instance?.status, metrics, serverState?.connected, profile.mcSemantics, t],
  )

  if (!instance) {
    /*
      【错误态与空态必须分开】原先是「服务器不存在或正在加载」一句盖住两种情况：
      后端 500 / 网络断时，运维看到的是「不存在」——会去查实例是否被删了，
      而事实只是这次没拉到。
    */
    if (instanceError) {
      return (
        <Card className="flex flex-col items-start gap-2 p-6 text-sm">
          <span role="alert" data-testid="instance-load-error" className="text-destructive">
            {t('serverConsole.loadFailed', '服务器信息加载失败')}
          </span>
          <Button size="sm" variant="outline" onClick={() => void refetchInstance()}>
            {t('common.retry')}
          </Button>
        </Card>
      )
    }
    return (
      <Card className="p-6 text-sm text-muted-foreground">
        {instanceLoading ? t('common.loading') : t('serverConsole.noInstance')}
      </Card>
    )
  }

  // 搭建中硬性禁启：provision 未终态期间启动按钮禁用 + tooltip 引导看任务中心，
  // 与后端启动闸（二轮②）同一信号源（statusReason「搭建中」），任务终态自然解禁。
  const provisioning = isProvisioningInstance(instance)
  // 重建在途：损毁实例重建期间 statusReason 标「重建中…」，据此禁用重建按钮、且不落红色失败横幅。
  // 判据已收敛到 `isRebuildingInstance`（与 `isProvisioningInstance` 同族，见其注释里的前缀匹配风险）——
  // 它自带 `status === 'DAMAGED'` 前提，故此处不再单独判 `isDamaged`。
  const rebuilding = isRebuildingInstance(instance)
  // 运行态漂移：>0 即存在未纳管活进程；无漂移时为 undefined（不渲染任何标记）。
  const runtimeDrift = runtimeDriftOf(instance)
  const overviewLogs = logs

  const setActiveTab = (tab: TabKey) => {
    if (tab === 'overview') setOverviewLogTime(Date.now())
    const next = new URLSearchParams(searchParams)
    if (tab === 'overview') next.delete('tab')
    else next.set('tab', tab)
    // 离开文件配置就清掉分段参数，避免 URL 残留无意义的 seg。
    if (tab !== 'resource') next.delete('seg')
    setSearchParams(next)
  }
  const setResourceSegment = (segment: ResourceSegment) => {
    const next = new URLSearchParams(searchParams)
    next.set('tab', 'resource')
    if (segment === 'files') next.delete('seg')
    else next.set('seg', segment)
    setSearchParams(next)
  }

  // 跳转统一走包内 renderLink 插槽（包内不认路由）：两处入口共用一套注入实现。
  const renderLink = ({ to, className, children }: InstanceConsoleLinkArgs) => (
    <Link to={to} className={className}>{children}</Link>
  )

  // 概览内的「动态与告警」流：崩溃现场区块自带快照/趋势取数，故整块由本层接线后注入面板。
  const activityFeed = (
    <InstanceActivityFeed
      instanceId={instance.id}
      logs={[]}
      watchItems={watchItems}
      uptimeSeconds={metrics?.uptimeSeconds}
    />
  )

  // 页签内容装配：每个页签对应一组自带取数的接线层组件，故由容器按页签注入，包内视图只提供 keep-alive 外壳。
  const renderTabPanel = (tab: TabKey): ReactNode => {
    if (tab === 'overview') {
      /* 概览：动态与告警流 + 指标条。468 起把「该实例当前的可操作面」
         两个面板并入概览——配额（限额来源 + 实时用量 + 强制状态）与二进制版本
         （受控升级 / 一级回滚）。选概览而非「进程」页签：overview 是所有画像（含未知
         回退）都存在的唯一 Tab，而 process 只在部分画像下出现——放 process 会让
         backend 画像（唯一没有 process 的画像）看不到这两个能力。 */
      return (
        <div className="space-y-3">
          <OverviewTpsData instanceUuid={instance.uuid} mcSemantics={profile.mcSemantics}>
            {(tpsPoints) => (
              <InstanceConsoleOverviewPanel
                instanceId={instance.id}
                metrics={metrics}
                online={online}
                maxPlayers={maxPlayers}
                playersAvailable={playersAvailable}
                maxPlayersAvailable={maxPlayersAvailable}
                nodeDiskUsage={node?.diskUsage}
                logs={overviewLogs}
                logsLoading={logsLoading}
                logsIncomplete={!!logFederation && logFederation.response.coverage?.complete !== true}
                logsError={logsError || !!logFederation?.degraded || !!logFederation?.failure || !!logFederation?.federatedNotReady}
                configuration={(
                  <div className="grid gap-2 lg:grid-cols-2">
                    <QuotaPanel instanceId={instance.id} />
                    <BinaryVersionPanel instanceId={instance.id} />
                  </div>
                )}
                watchItems={watchItems}
                probeConnected={serverState?.connected ?? false}
                mcSemantics={profile.mcSemantics}
                tpsPoints={tpsPoints}
                activityFeed={activityFeed}
                renderLink={renderLink}
              />
            )}
          </OverviewTpsData>
        </div>
      )
    }
    if (tab === 'resource') {
      /* 文件配置：文件管理器 + 环境变量两分段，均保活。 */
      return <InstanceResourceSegment instanceId={instance.id} segment={resourceSegment} onSegmentChange={setResourceSegment} />
    }
    if (tab === 'players') {
      /* 玩家分区（§2.3 按角色语义）：backend = 本实例单服实名名单；
         proxy 语义（画像含 bcTopology）= 跨服玩家分布（§2.2.2）。
         Tab 显隐仍只由 capabilities 决定，此处仅决定同一 Tab 内的呈现形态。 */
      return hasCapability(profile, 'bcTopology')
        ? <BcPlayersPanel instanceId={instance.id} />
        : <InstancePlayersSegment instanceId={instance.id} />
    }
    if (tab === 'backup') {
      /* 备份·定时分区接真：本实例定时任务启停/删 + 备份创建/恢复/删除。
起追加「整机快照」面板：快照底层复用同一套归档通道（全量备份 +
         回放），与备份同页签是单一归属——两者放一起运维才看得到「归档 vs 时间点」
         的分工，也不会在 backend/proxy/generic 三种画像里各缺一处。 */
      return (
        <div className="space-y-3">
          <InstanceBackupSegment instanceId={instance.id} />
          <SnapshotPanel instanceId={instance.id} />
        </div>
)
    }
    // BC 子服拓扑：子服列表 + 各自状态，由 bcTopology 能力驱动。
    // 跨服玩家/进程指标/config 分别归 players/process/config 页签（单一归属）。
    if (tab === 'bcTopology') return <BcSegment instanceId={instance.id} />
    // 进程视图：进程指标 + 启动参数。端口健康归 health 页签。
    if (tab === 'process') return <BinarySegment instanceId={instance.id} />
    // 端口 + 主动健康检查。
    if (tab === 'health') return <HealthPanel instanceId={instance.id} />
    // 结构化配置编辑（衔接）：BC config.yml / 原生二进制配置。
    if (tab === 'config') return <GenericConfigSegment instanceId={instance.id} />
    const cardType = TAB_CARD_TYPE[tab]
    if (!cardType) return null
    // 去掉原 min-h-[520px]：卡片吃满剩余高度，内部自行滚动。
    return (
      <Card className="min-h-0 flex-1 overflow-hidden py-0">
        {/* persistTerminal：终端连接由管理器常驻，页签隐藏/切换不断 WS。 */}
        <WorkspaceCardBody instanceId={instance.id} type={cardType} persistTerminal />
      </Card>
    )
  }

  return (
    <InstanceConsolePageView
      instance={instance}
      node={{ name: node?.name, diskUsage: node?.diskUsage }}
      metrics={metrics}
      players={{ online, maxPlayers, onlineAvailable: playersAvailable, maxAvailable: maxPlayersAvailable }}
      mcSemantics={profile.mcSemantics}
      visibleTabs={visibleTabs}
      activeTab={activeTab}
      onActiveTabChange={setActiveTab}
      mountedTabs={mountedTabs}
      provisioning={provisioning}
      rebuilding={rebuilding}
      canOperate={canOperate}
      actions={{
        start: () => setLifecycleConfirm('start'),
        rebuild: () => rebuild.mutate(instance.id),
        restart: () => setLifecycleConfirm('restart'),
        stop: () => setLifecycleConfirm('stop'),
        // 强杀只上报意图：二次确认由下方 DangerConfirm 承载，确认后才发请求。
        kill: () => setKillConfirmOpen(true),
      }}
      onNotify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
      renderLink={renderLink}
      runtimeDriftBanner={runtimeDrift ? (
        <RuntimeDriftBanner
          instanceId={instance.id}
          instanceName={instance.name}
          pid={runtimeDrift.pid}
          cmdline={runtimeDrift.cmdline}
          canOperate={canOperate}
        />
      ) : undefined}
      killConfirmDialog={(
        <>
          <DangerConfirm
            open={killConfirmOpen}
            title={t('danger.killInstanceTitle', { name: instance.name })}
            description={t('danger.killInstanceDesc')}
            confirmLabel={t('instances.kill')}
            scope="group"
            onConfirm={() => { kill.mutate(instance.id); setKillConfirmOpen(false) }}
            onCancel={() => setKillConfirmOpen(false)}
          />
          {/* 启/停/重启确认：与列表页同一口径（提示服务会下线/重启、玩家会掉线）。 */}
          <DangerConfirm
            open={lifecycleConfirm !== null}
            title={t('danger.instanceLifecycleTitle', {
              name: instance.name,
              action: lifecycleConfirm ? t(`instances.${lifecycleConfirm}`) : '',
            })}
            description={t('danger.instanceLifecycleDesc')}
            confirmLabel={lifecycleConfirm ? t(`instances.${lifecycleConfirm}`) : ''}
            scope="group"
            pending={start.isPending || stop.isPending || restart.isPending}
            onConfirm={() => {
              const action = lifecycleConfirm
              setLifecycleConfirm(null)
              if (action === 'start') start.mutate(instance.id)
              else if (action === 'stop') stop.mutate(instance.id)
              else if (action === 'restart') restart.mutate(instance.id)
            }}
            onCancel={() => setLifecycleConfirm(null)}
          />
        </>
      )}
      renderTabPanel={renderTabPanel}
    />
  )
}

/** 
 * 概览 TPS 火花线的取数（去 mock-api）。
 *
 * 单独成组件而非写在容器顶部：该查询原先挂在概览面板内，只有概览挂载时才建立；
 * 放在容器顶部会在实例尚未加载时先造一个 `targetId: ''` 的空查询，多一次无用订阅，
 * 也会让「等实例就绪」的等待信号被提前满足。这里用 render prop 把点位交回给面板，
 * 使取数时机与迁包前一致（仅概览页签、仅实例就绪后）。
 */
function OverviewTpsData({
  instanceUuid,
  mcSemantics,
  children,
}: {
  instanceUuid: string
  mcSemantics: boolean
  children: (tpsPoints: number[]) => ReactNode
}) {
  const { data } = useMetricSeries({
    scope: 'instance',
    targetId: instanceUuid,
    range: '1h',
    metrics: ['inst_tps'],
    enabled: mcSemantics,
  })
  // 取该实例的末段 avg 序列，缺测点丢弃（不补 0 假图）。折条与 aria 统计属展示，留包内面板。
  const tpsPoints = (data?.series.find((s) => s.metricKey === 'inst_tps' && s.world === '')?.points ?? [])
    .filter((p) => p.avg != null)
    .map((p) => p.avg as number)
  return <>{children(tpsPoints)}</>
}
