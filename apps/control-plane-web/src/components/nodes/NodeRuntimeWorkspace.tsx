import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Coffee, Package, ScrollText, Settings2 } from 'lucide-react'
import { Panel } from '@jianmanager/ui/components/panel'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
import { useNodeJDKsWithRefs, useDeleteJDK, useUpdateJDK } from '@/api/jdks'
import { useNodeRuntimes, useDeleteRuntime } from '@/api/runtimes'
import { useNodePMConfig } from '@/api/pmConfig'
import {
  useLogRuntime,
  useApprovedLogAssets,
  useControlLogRuntime,
  useInstallLogAsset,
  useUploadLogAsset,
  deriveLogDiskPct,
  lastValue,
  LOG_VL_DISK_USED,
  LOG_VL_DISK_LIMIT,
  LOG_VL_DISK_PCT,
  LOG_VL_PARTITIONS,
  LOG_VL_ROWS_DROPPED,
  type LogVLMetricSeries,
} from '@/api/logRuntime'
import { useMetricSeries } from '@/api/metrics'
import { buildRuntimeInventory, type RuntimeInventoryRow } from '@/lib/nodes/runtime-inventory'
import { evaluateReadiness, type ReadinessActionKey } from '@/lib/nodes/runtime-readiness'
import type { RuntimeSub } from '@/lib/nodes/runtime-sub'
import { RuntimeReadinessStrip } from '@/components/views/nodes/RuntimeReadinessStrip'
import { RuntimeInventoryTable } from '@/components/views/nodes/RuntimeInventoryTable'
import { RuntimeAddDrawer, type RuntimeAddTab } from '@/components/nodes/RuntimeAddDrawer'
import { LogRuntimeFlow, type LogRuntimeInstanceView } from '@/components/views/nodes/LogRuntimeFlow'
import NodePMConfigSection from '@/components/nodes/NodePMConfigSection'
import NodeGlobalPackagesTab from '@/components/nodes/NodeGlobalPackagesTab'
import NodeLogAcqPanel from '@/components/logs/NodeLogAcqPanel'
import type { NodeInfo } from '@/lib/nodes/node-types'

/**
 * 运行时工作台（节点详情 `?tab=runtime` 的内容）。
 *
 * 【为什么要一个工作台而不是三块面板竖排】改造前那个页签把 JDK 管理、运行时、
 * 日志平台运行时三块 700 行级面板纵向堆起来，每块内部还有自己的子页签与模态，
 * 结果是「页签里有页签、面板里有模态」，用户在层级里迷路，页面也不给任何结论。
 *
 * 现在按「一件事一个 sub」重组，二级寻址（`?sub=`）：
 *   运行时资产 | 包管理 | 日志平台 | 采集规则
 * 顶部常驻**就绪带**回答「这台节点能不能干活」，各 sub 只回答「怎么做」。
 *
 * 【取数纪律】
 *  - 只挂当前 sub 需要的查询（`enabled` 门控），切走即停——尤其是日志运行时的
 *    10s 轮询，改造前它无条件开着，用户不看日志也在打 Worker；
 *  - 就绪带需要的三份数据（JDK / 运行时 / 日志）是轻量的，无条件取（它要一直显示）；
 *  - 不在本页拉实例全量列表，引用关系由后端 `?withRefs=1` 一并算好。
 */

/** 就绪带默认指标窗口（与节点概览一致：24h）。 */
const READINESS_RANGE = '24h' as const

export interface NodeRuntimeWorkspaceProps {
  node: NodeInfo
  sub: RuntimeSub
  onSub: (next: RuntimeSub) => void
}

export default function NodeRuntimeWorkspace({ node, sub, onSub }: NodeRuntimeWorkspaceProps) {
  const { t } = useTranslation()
  const online = node.status === 1

  // ── 就绪带的数据源（轻量、常驻） ──
  // 就绪带要一直显示，所以这三份数据常驻（JDK/运行时/包管理器都是单请求、无轮询）；
  // 日志运行时与它 10s 的轮询只在 log sub 打开——那是改造前一直在跑的浪费。
  const jdksQ = useNodeJDKsWithRefs(node.id)
  const runtimesQ = useNodeRuntimes(node.id)
  const pmQ = useNodePMConfig(node.id)
  const logQ = useLogRuntime(node.id, sub === 'log')
  const assetsQ = useApprovedLogAssets(sub === 'log')
  /*
    日志运行时与审批资产**常驻**取数（log sub 内 10s、之外 60s 低频），
    因为就绪带要一直显示结论；关闭会让其它 sub 下这条误判成「审批资产未下发」。
  */


  // 日志存储指标（只在日志 sub 需要；就绪带在其它 sub 用缓存值即可，故这里也只在 log 打开）。
  const metricsQ = useMetricSeries({
    scope: 'node',
    targetId: node.uuid,
    range: READINESS_RANGE,
    metrics: [
      LOG_VL_DISK_USED,
      LOG_VL_DISK_LIMIT,
      LOG_VL_DISK_PCT,
      LOG_VL_PARTITIONS,
      LOG_VL_ROWS_DROPPED,
    ],
    enabled: sub === 'log',
  })

  const logSeries = (metricsQ.data?.series ?? []) as LogVLMetricSeries[]
  const logDiskPct = sub === 'log' ? deriveLogDiskPct(logSeries) : null
  const logDiskUsed = sub === 'log' ? lastValue(logSeries, LOG_VL_DISK_USED) : null
  // 上限只在「字节口径」下有意义：百分比部署时 limit 的 unit 是 pct（值如 95），
  // 当成字节显示会印出「95 B」这种荒谬数字，故用「比已用大一个量级」来甄别。
  const rawLimit = sub === 'log' ? lastValue(logSeries, LOG_VL_DISK_LIMIT) : null
  const logDiskLimit = rawLimit != null && logDiskUsed != null && rawLimit > logDiskUsed ? rawLimit : null
  const logPartitions = sub === 'log' ? lastValue(logSeries, LOG_VL_PARTITIONS) : null
  const logRowsDropped = sub === 'log' ? lastValue(logSeries, LOG_VL_ROWS_DROPPED) : null
  // 趋势：把 pct 序列点摊成数组给 Sparkline（无 pct 时用 used 字节，曲线形状仍有意义）。
  const logTrend = (() => {
    const key = logDiskPct != null ? LOG_VL_DISK_PCT : LOG_VL_DISK_USED
    const s = logSeries.find((x) => x.metricKey === key)
    return (s?.points ?? []).map((pt) => pt.avg).filter((v): v is number => typeof v === 'number' && Number.isFinite(v))
  })()

  const normalizedArch = node.arch.toLowerCase() === 'x64' ? 'amd64' : node.arch.toLowerCase()
  const approvedAsset = (assetsQ.data ?? []).find(
    (a) => a.os === node.os.toLowerCase() && a.arch === normalizedArch,
  )

  const readiness = useMemo(
    () =>
      evaluateReadiness({
        nodeOnline: online,
        jdks: jdksQ.data?.map((j) => ({ managed: j.managed, majorVersion: j.majorVersion, refCount: j.refCount })),
        runtimes: runtimesQ.data?.map((r) => ({ type: r.type, majorVersion: r.majorVersion })),
        packages: { pm: pmQ.data?.pm ?? null },
        logRuntime: logQ.data ?? null,
        logAssetCached: approvedAsset?.cached,
        logDiskPct,
        logDiskUsedBytes: logDiskUsed,
        loaded: {
          jdks: !jdksQ.isLoading,
          runtimes: !runtimesQ.isLoading,
          log: !logQ.isLoading,
        },
      }),
    [online, jdksQ.data, jdksQ.isLoading, runtimesQ.data, runtimesQ.isLoading, pmQ.data, logQ.data, logQ.isLoading, approvedAsset?.cached, logDiskPct, logDiskUsed],
  )

  // ── 运行时资产 ──
  const delJDK = useDeleteJDK(node.id)
  const updateJDK = useUpdateJDK(node.id)
  const delRuntime = useDeleteRuntime(node.id)
  const [addOpen, setAddOpen] = useState(false)
  const [addTab, setAddTab] = useState<RuntimeAddTab>('jdk')
  const [editing, setEditing] = useState<RuntimeInventoryRow | null>(null)
  const [pendingDelete, setPendingDelete] = useState<RuntimeInventoryRow | null>(null)

  const inventory = useMemo(
    () => buildRuntimeInventory(jdksQ.data ?? [], runtimesQ.data ?? []),
    [jdksQ.data, runtimesQ.data],
  )

  // ── 日志平台 ──
  const control = useControlLogRuntime(node.id)
  const installAsset = useInstallLogAsset(node.id)
  const uploadAsset = useUploadLogAsset()

  /*
    【为什么删掉了 canMigrate】迁移分区（HOT→COLD）随冷热双层废除已无业务语义：
    后端 migrator 要求 target_tier=cold，而 cold 层不再存在。原先的平台管理员门禁
    只为这一个动作存在，动作移除后门禁一并移除。归档回灌仍保留在应急区，
    它有自己的 worker 侧 capability 判定（未装配时后端明确拒绝）。
  */

  /*
    后端只返回唯一的受管实例（冷热双层 2026-10-29 已废除），所以直接映射全部实例，
    不做 hot/cold/rehydrate 的分层展开。容量指标是**节点级**的（整个 VL 数据目录），
    与实例一一对应，正好可以挂在这一张卡上。
  */
  const logInstances: LogRuntimeInstanceView[] = (logQ.data?.instances ?? []).map((inst) => ({
    namespace: inst.namespace,
    state: inst.state,
    health_ok: inst.health_ok,
    query_ready: inst.query_ready,
    partition_recovery_complete: inst.partition_recovery_complete,
    listen_addr: inst.listen_addr,
    asset_tag: inst.asset_tag,
    last_error: inst.last_error,
    diskUsedBytes: logDiskUsed,
    diskPct: logDiskPct,
    diskLimitBytes: logDiskLimit,
    diskTrend: logTrend,
    partitions: logPartitions,
    rowsDropped: logRowsDropped,
  }))

  const errMsg = (err: unknown, fallback: string) =>
    (err as { response?: { data?: { message?: string } } })?.response?.data?.message || fallback

  /** 就绪带上的主行动：按 key 落到对应的 sub 或直接触发动作。 */
  const handleReadinessAction = (key: ReadinessActionKey) => {
    switch (key) {
      case 'installJDK':
      case 'registerJDK':
        setAddTab('jdk')
        setAddOpen(true)
        break
      case 'installNode':
        setAddTab('jdk')
        setAddOpen(true)
        break
      case 'scanRuntimes':
        setAddTab('scan')
        setAddOpen(true)
        break
      case 'dispatchLogAsset':
      case 'startLogRuntime':
      case 'openLogStorage':
        onSub('log')
        break
      case 'openLogAcq':
        onSub('logAcq')
        break
    }
  }

  const submitDelete = async () => {
    const row = pendingDelete
    if (!row) return
    try {
      if (row.type === 'jdk') await delJDK.mutateAsync(row.id)
      else await delRuntime.mutateAsync({ id: row.id, type: row.type })
      toast.success(t('nodes.runtimeLib.deleted', '运行时已删除'))
      setPendingDelete(null)
    } catch (err) {
      toast.error(errMsg(err, t('nodes.runtimeLib.deleteFailed', '删除失败')))
    }
  }

  return (
    <div className="space-y-3">
      <RuntimeReadinessStrip items={readiness} onAction={handleReadinessAction} />

      {/* 二级 Segments：一件事一个 sub。 */}
      <Segments aria-label={t('nodes.runtime.sections', '运行时分段')}>
        <Segment active={sub === 'inventory'} onClick={() => onSub('inventory')} data-testid="runtime-sub-inventory">
          <Coffee className="mr-1 inline size-3.5" aria-hidden />
          {t('nodes.runtime.sub.inventory', '运行时资产')}
        </Segment>
        <Segment active={sub === 'packages'} onClick={() => onSub('packages')} data-testid="runtime-sub-packages">
          <Package className="mr-1 inline size-3.5" aria-hidden />
          {t('nodes.runtime.sub.packages', '包管理')}
        </Segment>
        <Segment active={sub === 'log'} onClick={() => onSub('log')} data-testid="runtime-sub-log">
          <ScrollText className="mr-1 inline size-3.5" aria-hidden />
          {t('nodes.runtime.sub.log', '日志平台')}
        </Segment>
        <Segment active={sub === 'logAcq'} onClick={() => onSub('logAcq')} data-testid="runtime-sub-logAcq">
          <Settings2 className="mr-1 inline size-3.5" aria-hidden />
          {t('nodes.runtime.sub.logAcq', '采集规则')}
        </Segment>
      </Segments>

      {sub === 'inventory' && (
        <Panel title={t('nodes.runtime.inventoryTitle', '运行时清单')}>
          <RuntimeInventoryTable
            rows={inventory}
            isLoading={jdksQ.isLoading || runtimesQ.isLoading}
            onOpenInstall={() => {
              setAddTab('jdk')
              setAddOpen(true)
            }}
            onOpenScan={() => {
              setAddTab('scan')
              setAddOpen(true)
            }}
            onEdit={(row) => setEditing(row)}
            onDelete={(row) => setPendingDelete(row)}
            onCopyPath={(p) => {
              void navigator.clipboard?.writeText(p).then(
                () => toast.success(t('artifactCache.pathCopied', '路径已复制')),
                () => toast.error(t('common.copyFailed', '复制失败')),
              )
            }}
          />
        </Panel>
      )}

      {sub === 'packages' && (
        <div className="space-y-3">
          <Panel title={t('pmConfig.title', '包管理器与 registry')}>
            <NodePMConfigSection nodeId={node.id} active />
          </Panel>
          <Panel title={t('pkg.title', '全局包')}>
            <NodeGlobalPackagesTab nodeId={node.id} active />
          </Panel>
        </div>
      )}

      {sub === 'log' && (
        <Panel title={t('logsRuntime.title', '日志平台运行时')}>
          <LogRuntimeFlow
            view={{
              os: node.os,
              arch: normalizedArch,
              online,
              instances: logInstances,
              assetCached: !!approvedAsset?.cached,
              assetTag: approvedAsset?.tag,
              assetSha: approvedAsset?.executableSha256,
              error: logQ.isError ? errMsg(logQ.error, t('logsRuntime.opFailed', '操作失败')) : null,
            }}
            busy={control.isPending || installAsset.isPending || uploadAsset.isPending}
            pending={{
              upload: uploadAsset.isPending,
              install: installAsset.isPending,
              control: control.isPending,
            }}
            fetching={logQ.isFetching}
            onRefresh={() => {
              void logQ.refetch()
              void assetsQ.refetch()
              void metricsQ.refetch()
            }}
            onUpload={(file) => {
              void uploadAsset
                .mutateAsync({ os: node.os.toLowerCase(), arch: normalizedArch, file })
                .then(() => toast.success(t('logsRuntime.assetCached', '审批包已缓存')))
                .catch((err) => toast.error(errMsg(err, t('logsRuntime.opFailed', '操作失败'))))
            }}
            onInstall={() => {
              void installAsset
                .mutateAsync(undefined)
                .then(() => toast.success(t('logsRuntime.assetInstalled', '受管资产已安装')))
                .catch((err) => toast.error(errMsg(err, t('logsRuntime.opFailed', '操作失败'))))
            }}
            onControl={(ns, action) => {
              void control
                .mutateAsync({ namespace: ns, action })
                .then(() => toast.success(t(`logsRuntime.action.${action}`, '已完成')))
                .catch((err) => toast.error(errMsg(err, t('logsRuntime.opFailed', '操作失败'))))
            }}
          />
        </Panel>
      )}

      {sub === 'logAcq' && (
        <Panel title={t('nodeLogAcq.title', '日志采集规则')}>
          {/* 这个面板原本零引用（写好但没入口）；这里挂上来，让「采集规则」在节点内可达。 */}
          <NodeLogAcqPanel nodeId={node.id} active online={online} />
        </Panel>
      )}

      <RuntimeAddDrawer
        open={addOpen}
        onOpenChange={setAddOpen}
        nodeId={node.id}
        online={online}
        tab={addTab}
        onTab={setAddTab}
        editing={editing}
        onCloseEdit={() => setEditing(null)}
        pendingDelete={pendingDelete}
        onCloseDelete={() => setPendingDelete(null)}
        onConfirmDelete={() => void submitDelete()}
        onSaveEdit={async (id, body) => {
          try {
            await updateJDK.mutateAsync({ jdkId: id, body })
            toast.success(t('nodes.jdkEditSaved', '已保存 JDK 登记信息'))
            setEditing(null)
            return true
          } catch (err) {
            toast.error(errMsg(err, t('nodes.jdkEditFailed', '保存失败')))
            return false
          }
        }}
        deleting={delJDK.isPending || delRuntime.isPending}
      />
    </div>
  )
}
