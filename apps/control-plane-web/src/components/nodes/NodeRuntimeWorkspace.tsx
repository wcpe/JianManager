import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Archive, Coffee, Package, ScrollText, Settings2 } from 'lucide-react'
import { Panel } from '@jianmanager/ui/components/panel'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { useNodeJDKsWithRefs, useDeleteJDK, useUpdateJDK } from '@/api/jdks'
import { useNodeRuntimes, useDeleteRuntime } from '@/api/runtimes'
import { useNodePMConfig } from '@/api/pmConfig'
import { useLogRuntime, useApprovedLogAssets, useControlLogRuntime, useInstallLogAsset, useMigrateLogPartition, useResolveLogIngestGaps, useUploadLogAsset, deriveLogDiskPct, lastValue, LOG_VL_DISK_USED, type LogVLMetricSeries } from '@/api/logRuntime'
import { useMetricSeries } from '@/api/metrics'
import { buildRuntimeInventory, type RuntimeInventoryRow } from '@/lib/nodes/runtime-inventory'
import { evaluateReadiness, type ReadinessActionKey } from '@/lib/nodes/runtime-readiness'
import type { RuntimeSub } from '@/lib/nodes/runtime-sub'
import { RuntimeReadinessStrip } from '@/components/views/nodes/RuntimeReadinessStrip'
import { RuntimeInventoryTable } from '@/components/views/nodes/RuntimeInventoryTable'
import { RuntimeAddDrawer } from '@/components/nodes/RuntimeAddDrawer'
import { LogRuntimeFlow, type LogNamespace, type LogRuntimeLayerView } from '@/components/views/nodes/LogRuntimeFlow'
import NodePMConfigSection from '@/components/nodes/NodePMConfigSection'
import NodeGlobalPackagesTab from '@/components/nodes/NodeGlobalPackagesTab'
import NodeLogAcqPanel from '@/components/logs/NodeLogAcqPanel'
import LogMigrateDialog from '@/components/nodes/LogMigrateDialog'
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
    metrics: [LOG_VL_DISK_USED, 'log_vl_disk_limit', 'log_vl_disk_pct', 'log_vl_partitions'],
    enabled: sub === 'log',
  })

  const logSeries = (metricsQ.data?.series ?? []) as LogVLMetricSeries[]
  const logDiskPct = sub === 'log' ? deriveLogDiskPct(logSeries) : null
  const logDiskUsed = sub === 'log' ? lastValue(logSeries, LOG_VL_DISK_USED) : null

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
  const [addTab, setAddTab] = useState<'install' | 'register' | 'scan'>('install')
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
  const migrate = useMigrateLogPartition(node.id)
  const resolveGaps = useResolveLogIngestGaps(node.id)
  const [migrateOpen, setMigrateOpen] = useState(false)

  const jwtRole = useAuthStore((s) => s.role)
  const permLoaded = usePermissionsStore((s) => s.loaded)
  const permIsAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  // 判定留在外壳：依赖 auth/me + JWT 双源，属应用侧知识（与改造前同口径，未搬动判据）。
  const canMigrate = permLoaded ? permIsAdmin : jwtRole === 10

  const logLayers: LogRuntimeLayerView[] = (['hot', 'cold', 'rehydrate'] as LogNamespace[]).map((ns) => {
    const inst = logQ.data?.instances.find((i) => i.namespace === ns)
    return {
      namespace: ns,
      state: inst?.state,
      health_ok: inst?.health_ok,
      query_ready: inst?.query_ready,
      listen_addr: inst?.listen_addr,
      asset_tag: inst?.asset_tag,
      last_error: inst?.last_error,
      // 容量指标是节点级的（VL 数据目录整体），不对单层拆分——所以只在 HOT 上展示，
      // 其余层留空。给每层都填同一个数会让人以为是三层各自的用量。
      diskUsedBytes: ns === 'hot' ? logDiskUsed : null,
      diskPct: ns === 'hot' ? logDiskPct : null,
    }
  })

  const errMsg = (err: unknown, fallback: string) =>
    (err as { response?: { data?: { message?: string } } })?.response?.data?.message || fallback

  /** 就绪带上的主行动：按 key 落到对应的 sub 或直接触发动作。 */
  const handleReadinessAction = (key: ReadinessActionKey) => {
    switch (key) {
      case 'installJDK':
      case 'registerJDK':
        setAddTab('install')
        setAddOpen(true)
        break
      case 'installNode':
        setAddTab('install')
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
              setAddTab('install')
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
              layers: logLayers,
              assetCached: !!approvedAsset?.cached,
              assetTag: approvedAsset?.tag,
              assetSha: approvedAsset?.executableSha256,
              error: logQ.isError ? errMsg(logQ.error, t('logsRuntime.opFailed', '操作失败')) : null,
            }}
            busy={control.isPending || migrate.isPending || resolveGaps.isPending || installAsset.isPending || uploadAsset.isPending}
            pending={{
              upload: uploadAsset.isPending,
              install: installAsset.isPending,
              control: control.isPending,
              migrate: migrate.isPending,
              gaps: resolveGaps.isPending,
            }}
            fetching={logQ.isFetching}
            canMigrate={canMigrate}
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
            onOpenMigrate={() => setMigrateOpen(true)}
            onResolveGaps={() => {
              void resolveGaps
                .mutateAsync(undefined)
                .then(() => toast.success(t('logsRuntime.gapsResolved', '已核销已被投影覆盖的缺口')))
                .catch((err) => toast.error(errMsg(err, t('logsRuntime.opFailed', '操作失败'))))
            }}
            onRehydrate={() => {
              // 回灌入口落在日志页的归档管理（跨节点视角），这里给出直接跳转而不是重复实现一遍。
              toast.info(t('logsRuntime.archive.openLogs', '请在「日志」页的归档管理中发起回灌'))
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

      <LogMigrateDialog
        /* key 随开关变化：每次打开都是全新实例（日期与错误归零），不靠 effect 清 state。 */
        key={migrateOpen ? 'migrate-open' : 'migrate-closed'}
        open={migrateOpen}
        onOpenChange={setMigrateOpen}
        nodeId={node.id}
        pending={migrate.isPending}
        canMigrate={canMigrate}
        onConfirm={async (utcDay) => {
          try {
            await migrate.mutateAsync({ storageNamespace: `node:${node.id}`, utcDay })
            toast.success(t('logsRuntime.migrateDone', '分区已迁移到 COLD'))
            return null
          } catch (err) {
            return errMsg(err, t('logsRuntime.opFailed', '操作失败'))
          }
        }}
      />
      {/* 归档管理入口（回灌）在日志页；这里给一个直达提示，避免用户以为「没有这个功能」。 */}
      <p className="flex items-center gap-1 text-[10px] text-muted-foreground/70">
        <Archive className="size-3" aria-hidden />
        {t('logsRuntime.archive.hint', '归档回灌与分区清理在「日志」页的归档管理中操作（跨节点视角）。')}
      </p>
    </div>
  )
}
