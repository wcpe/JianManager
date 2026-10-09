/**
 * @file RuntimeAssetsPageView：运行时与制品全局页（JDK 跨节点矩阵 + 制品库）的受控视图——聚合载荷、
 *       刷新/删除/导入/批量部署四类写动作与 toast 文案全由应用容器注入，实例候选与制品对账区块经插槽注入。
 * @input lib/runtime-assets-view（formatBytes / buildRuntimeGrid / filterAssetGroups / shortSha /
 *        DEFAULT_ASSET_FILTER / RUNTIME_TYPE_LABEL / AssetFilter）、lib/asset-contracts（AssetInfo / AssetType）、
 *        lib/runtime-assets-contracts（JDKMatrixItem / AssetTypeGroup / RuntimeMatrixEntry）、
 *        lib/relative-time（formatRelativeTime）、lib/threshold（instanceStatusLevel / StatusLevel）、
 *        Panel / Button / Input / Dialog / scrollable-dialog / Table / EmptyState / layout 原语、
 *        views/DangerConfirm（删除二次确认）、翻译上下文
 * @output RuntimeAssetsPageView、RuntimeAssetsPageViewProps、RuntimeAssetsOverviewView、RuntimeAssetsChannelRefView、
 *         RuntimeAssetsJDKRef、RuntimeAssetImportArgs、RuntimeAssetDeleteArgs、RuntimeAssetBatchDeployArgs、
 *         PluginBatchDeployResultView、RuntimeAssetsInstancePickerView、RuntimeAssetsInstancePickerViewProps、
 *         RuntimeAssetsInstancePickerArgs、RuntimeAssetsInstanceCandidateView
 * @sync apps/control-plane-web/src/pages/RuntimeAssetsPage.tsx、apps/control-plane-web/src/pages/RuntimeAssetsPage.dom.test.tsx、
 *       apps/control-plane-web/src/pages/RuntimeAssetsPage.fr301.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-082 运行时与制品全局页、FR-301 多运行时矩阵与手动刷新、
 *        FR-155 制品导入上传进度、FR-033/FR-045 引用保护与删除占用方提示）
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Coffee, Cpu, Package, RefreshCw, SearchX, Trash2, Upload } from 'lucide-react'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/views/DangerConfirm'
import type { AssetInfo, AssetType } from '@jianmanager/ui/lib/asset-contracts'
import type { AssetTypeGroup, JDKMatrixItem, RuntimeMatrixEntry } from '@jianmanager/ui/lib/runtime-assets-contracts'
import { formatRelativeTime } from '@jianmanager/ui/lib/relative-time'
import { instanceStatusLevel, type StatusLevel } from '@jianmanager/ui/lib/threshold'
import { cn } from '@jianmanager/ui'
import {
  buildRuntimeGrid,
  DEFAULT_ASSET_FILTER,
  filterAssetGroups,
  formatBytes,
  RUNTIME_TYPE_LABEL,
  shortSha,
  type AssetFilter,
} from '@jianmanager/ui/lib/runtime-assets-view'

/** 状态等级 → 色点类（实例状态前导点）。 */
const LEVEL_DOT: Record<StatusLevel, string> = {
  success: 'bg-status-success',
  warning: 'bg-status-warning',
  danger: 'bg-status-danger',
  info: 'bg-status-info',
  neutral: 'bg-muted-foreground',
}

/** 制品类型筛选选项（含「全部」）。 */
const ASSET_TYPES: Array<AssetType | 'all'> = [
  'all',
  'core',
  'plugin',
  'image',
  'video',
  'archive',
  'blob',
  'client-file',
]

/** 可导入的制品类型（不含 client-file：客户端文件走分发页专属发布流程，FR-088/251）。 */
const IMPORTABLE_ASSET_TYPES: AssetType[] = ['core', 'plugin', 'image', 'video', 'archive', 'blob']

/** 已上传字节 / 总字节 → 百分比（0~100，钳制）。 */
function importPercent(loaded: number, total: number): number {
  if (total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((loaded / total) * 100)))
}

/**
 * 制品 metadata（JSON 串）里客户端文件用的展示字段。
 * 解析失败按空对象处理：metadata 是历史遗留的自由文本，坏值不应拖垮整行渲染。
 */
function parseAssetMetadata(raw: string): { path?: string; codec?: string } {
  if (!raw) return {}
  try {
    const value = JSON.parse(raw) as { path?: string; targetPath?: string; codec?: string }
    return { path: value.path || value.targetPath, codec: value.codec }
  } catch {
    return {}
  }
}

/** 制品外置渠道的展示引用（只需 id/name 两字段：存储位置列按 id 反查渠道名）。 */
export interface RuntimeAssetsChannelRefView {
  id: number
  name: string
  type: string
}

/** JDK 删除目标（nodeId 定位节点、jdkId 定位该节点上的 JDK 记录）。 */
export interface RuntimeAssetsJDKRef {
  nodeId: number
  jdkId: number
}

/** 运行时/制品聚合载荷（本视图渲染所需的字段子集；容器可直接传 API 完整对象）。 */
export interface RuntimeAssetsOverviewView {
  jdks: JDKMatrixItem[]
  jdkSummary: { nodeCount: number; jdkCount: number; referencedJdk: number; instanceRefs: number }
  assets: AssetTypeGroup[]
  assetSummary: {
    assetCount: number
    totalSize: number
    referencedCount: number
    hotCount: number
    archivedCount: number
    externalCount: number
    lostCount: number
  }
  /** 多运行时矩阵项（FR-301）：jdk + nodejs 等多类型。 */
  runtimes: RuntimeMatrixEntry[]
  artifactChannels: RuntimeAssetsChannelRefView[]
  /** 整体上次库存同步时间（ISO）；null=从未同步。 */
  syncedAt: string | null
}

/**
 * 导入制品请求：上传编排在容器（multipart 入库 + overview 失效），视图只回传草稿与两个进度钩子。
 *
 * 进度经 `onProgress` 回流、请求落定（成功或失败）经 `onSettled` 回调——与 `PluginManager` 的
 * 上传契约同款：进度条是纯展示状态，留在视图内即可，容器不必为此多持一份状态。
 */
export interface RuntimeAssetImportArgs {
  type: AssetType
  file: File
  /** 空串表示不指定（与原页 `name.trim() || undefined` 同口径）。 */
  name?: string
  /** 同上。 */
  version?: string
  /** 已上传 / 总字节（axios onUploadProgress 口径）。 */
  onProgress: (loaded: number, total: number) => void
  /** 请求结束（成功或失败）回调：视图据此收起进度条。 */
  onSettled: () => void
}

/**
 * 删除制品的上报参数：`refCount` 一并上报，供容器在 409 响应缺 `count` 时按行上的引用数兜底
 * （迁包前该提示正是 `err.response.data?.count ?? asset.refCount`）。
 */
export interface RuntimeAssetDeleteArgs {
  assetId: number
  refCount: number
}

/** 批量部署请求：插件制品 → 多实例 plugins/ 目录（destination/overwrite 沿用后端缺省）。 */
export interface RuntimeAssetBatchDeployArgs {
  assetIds: number[]
  target: { ids: number[] }
}

/** 批量部署回执（本视图只渲染计数与逐条结果，故只声明用到的字段）。 */
export interface PluginBatchDeployResultView {
  success: number
  skipped: number
  failed: number
  results: Array<{ id: number | string; name: string; error?: string; reason?: string }>
}

/** 实例候选行（候选窗口由容器取数注入；本视图只消费这四个字段）。 */
export interface RuntimeAssetsInstanceCandidateView {
  id: number
  name: string
  status: string
  nodeId: number
}

/**
 * 实例候选插槽参数：本视图只声明「候选区需要什么、勾选怎么回传」，
 * 「何时发请求、一次取多少条」是容器策略（千级实例必须走服务端搜索，不得一次拉全量）。
 *
 * 【为什么不是单选 `InstancePicker`】批量部署要「勾选多个 → 部署到 N 个实例」，
 * 单选 Combobox 无法表达多选语义；故与 `NetworkInstancePickerView` 同款：候选列表本体在包内，
 * 取数与关键字状态留在容器。
 */
export interface RuntimeAssetsInstancePickerArgs {
  /** 已勾选的实例 id（多选：一次可部署到多个实例）。 */
  selected: number[]
  /** 勾选变更上报。 */
  onToggle: (id: number, on: boolean) => void
}

/** 实例候选选择器（多选 + 服务端搜索）：筛选框 + 候选勾选列表。 */
export interface RuntimeAssetsInstancePickerViewProps {
  /** 候选窗口（容器取数注入）；缺省按空候选渲染。 */
  items?: RuntimeAssetsInstanceCandidateView[]
  /** 候选取数中（占位「加载目标实例…」）。 */
  loading?: boolean
  /** 已勾选的实例 id（受控：勾选与「部署到 N 个实例」计数同源，由弹窗持有）。 */
  selected: number[]
  /** 勾选变更上报。 */
  onToggle: (id: number, on: boolean) => void
  /** 键入关键字上报（容器下发服务端 q）。 */
  onQueryChange: (keyword: string) => void
}

/**
 * 实例候选选择器：搜索框 + 勾选列表（含加载/空候选占位）。
 *
 * 输入框的显示值留本组件（纯展示草稿），关键字经 `onQueryChange` 上报给容器；
 * 候选窗口与总数由容器按服务端结果注入，本地不做全量兜底。
 */
export function RuntimeAssetsInstancePickerView({
  items,
  loading = false,
  selected,
  onToggle,
  onQueryChange,
}: RuntimeAssetsInstancePickerViewProps) {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  const instances = items ?? []

  return (
    <>
      <Input
        value={keyword}
        onChange={(e) => {
          setKeyword(e.target.value)
          onQueryChange(e.target.value)
        }}
        placeholder={t('plugins.batchDeploy.targetSearch')}
        className="h-8 text-xs"
      />
      <div className="max-h-72 space-y-2 overflow-auto rounded border p-2">
        {loading && (
          <p className="p-2 text-sm text-muted-foreground">{t('plugins.batchDeploy.loadingTargets')}</p>
        )}
        {!loading && instances.length === 0 && (
          <p className="p-2 text-sm text-muted-foreground">{t('plugins.batchDeploy.noTargets')}</p>
        )}
        {instances.map((inst) => (
          <label key={inst.id} className="flex cursor-pointer items-start gap-2 rounded p-2 hover:bg-muted/60">
            <input
              type="checkbox"
              className="mt-1"
              checked={selected.includes(inst.id)}
              onChange={(e) => onToggle(inst.id, e.target.checked)}
            />
            <span className="min-w-0 text-sm">
              <span className="block font-medium">{inst.name}</span>
              <span className="block text-xs text-muted-foreground">
                #{inst.id} · {inst.status} · {t('runtimeAssets.node')} {inst.nodeId}
              </span>
            </span>
          </label>
        ))}
      </div>
    </>
  )
}

/**
 * 运行时与制品全局页（FR-082）受控视图：JDK 区（矩阵 + 卡片 + 删除）+ 制品区（统计 + 筛选 + 表格 +
 * 导入弹窗 + 批量部署弹窗）+ 对账区块插槽。
 *
 * 受控边界：聚合载荷与全部写动作由容器注入；视图内只留纯 UI 状态——筛选草稿（本地过滤，不触发取数）、
 * 两个对话框的开合与草稿、勾选集合、待确认的删除目标。会触发重新取数的实例搜索词在容器侧
 * （插槽实现自带），本视图不持有。
 */
export interface RuntimeAssetsPageViewProps {
  /** 聚合载荷（容器注入 `useRuntimeAssetsOverview().data`）；未拿到时为 undefined。 */
  overview?: RuntimeAssetsOverviewView
  /** 首次取数中：渲染加载文案（与原页一致，此时不渲染页壳）。 */
  isLoading: boolean
  /** 取数失败（含无权限 403）：渲染错误文案。 */
  isError: boolean
  /** 手动强制同步各节点运行时库存（JDK 区「刷新」按钮）。 */
  onRefreshRuntimes: () => void
  /** 刷新在途（按钮禁用 + 图标转圈）。 */
  refreshing: boolean
  /** 删除某节点上的一个 JDK；视图已完成二次确认。 */
  onDeleteJdk: (target: RuntimeAssetsJDKRef) => void
  /** 正在删除的 JDK 记录 id；无在途为 null（逐卡片禁用，与迁包前各自的 mutation 实例等价）。 */
  deletingJdkId: number | null
  /** 删除一条制品；视图已完成二次确认，并如实带上行上的引用数（409 兜底口径）。 */
  onDeleteAsset: (args: RuntimeAssetDeleteArgs) => void
  /** 正在删除的制品 id；无在途为 null（逐行禁用）。 */
  deletingAssetId: number | null
  /** 导入一条制品（含上传进度钩子）；返回是否成功——成功才关窗，失败保留草稿。 */
  onImportAsset: (args: RuntimeAssetImportArgs) => Promise<boolean>
  /** 导入在途（表单与提交按钮禁用）。 */
  importing: boolean
  /** 批量部署插件制品到多实例。 */
  onBatchDeploy: (args: RuntimeAssetBatchDeployArgs) => void
  /** 批量部署在途（提交按钮禁用 + 文案切换）。 */
  deploying: boolean
  /** 批量部署回执（容器注入 mutation 结果）；null 表示本次尚未提交。 */
  deployResult: PluginBatchDeployResultView | null
  /** 实例候选插槽（千级实例须服务端搜索，取数与关键字状态由容器实现自带）。 */
  renderInstancePicker: (args: RuntimeAssetsInstancePickerArgs) => ReactNode
  /** 制品存储对账区块插槽（FR-349）：取数与处置动作在应用侧接线层，故经插槽注入。 */
  renderReconcileSection: (args: { channels: RuntimeAssetsChannelRefView[] }) => ReactNode
}

/**
 * 运行时与制品全局页：两区——JDK 跨节点矩阵 + 引用实例；制品按类型占用/去重/冷热。
 * 删除受引用项由后端拒绝并指出占用方（FR-033/045 引用保护），视图只负责把原因呈现为提示。
 */
export function RuntimeAssetsPageView({
  overview,
  isLoading,
  isError,
  onRefreshRuntimes,
  refreshing,
  onDeleteJdk,
  deletingJdkId,
  onDeleteAsset,
  deletingAssetId,
  onImportAsset,
  importing,
  onBatchDeploy,
  deploying,
  deployResult,
  renderInstancePicker,
  renderReconcileSection,
}: RuntimeAssetsPageViewProps) {
  const { t } = useTranslation()

  if (isLoading) {
    return <p className="text-muted-foreground">{t('common.loading')}</p>
  }
  if (isError || !overview) {
    return <p className="text-destructive">{t('runtimeAssets.loadFailed')}</p>
  }

  return (
    // 全量对齐：外壳与页头改用布局层原语。原先无 data-page，迁移时补上。
    <PageShell data-page="runtime-assets">
      <PageHeader
        title={t('runtimeAssets.title')}
        description={t('runtimeAssets.subtitle')}
      />

      <JDKSection
        jdks={overview.jdks}
        summary={overview.jdkSummary}
        runtimes={overview.runtimes}
        syncedAt={overview.syncedAt}
        onRefresh={onRefreshRuntimes}
        refreshing={refreshing}
        onDeleteJdk={onDeleteJdk}
        deletingJdkId={deletingJdkId}
      />
      <AssetSection
        groups={overview.assets}
        summary={overview.assetSummary}
        channels={overview.artifactChannels}
        onDeleteAsset={onDeleteAsset}
        deletingAssetId={deletingAssetId}
        onImportAsset={onImportAsset}
        importing={importing}
        onBatchDeploy={onBatchDeploy}
        deploying={deploying}
        deployResult={deployResult}
        renderInstancePicker={renderInstancePicker}
      />
      {renderReconcileSection({ channels: overview.artifactChannels })}
    </PageShell>
  )
}

/** 一个汇总指标小卡（数字 + 标签）。 */
function StatCard({ label, value, accent }: { label: string; value: ReactNode; accent?: boolean }) {
  return (
    <div className="rounded-md border bg-card px-3 py-2">
      <div className={cn('text-lg font-bold tabular-nums', accent && 'text-primary')}>{value}</div>
      <div className="text-[11px] text-muted-foreground">{label}</div>
    </div>
  )
}

/* ============================ JDK 区 ============================ */

function JDKSection({
  jdks,
  summary,
  runtimes,
  syncedAt,
  onRefresh,
  refreshing,
  onDeleteJdk,
  deletingJdkId,
}: {
  jdks: JDKMatrixItem[]
  summary: { nodeCount: number; jdkCount: number; referencedJdk: number; instanceRefs: number }
  /** 多运行时矩阵项（FR-301）：jdk + nodejs 等多类型。 */
  runtimes: RuntimeMatrixEntry[]
  /** 整体上次库存同步时间（ISO）；null=从未同步。 */
  syncedAt: string | null
  /** 手动刷新（FR-301）：强制全节点 syncFromWorker。失败容忍由容器提示，本视图不转错误态。 */
  onRefresh: () => void
  refreshing: boolean
  onDeleteJdk: (target: RuntimeAssetsJDKRef) => void
  deletingJdkId: number | null
}) {
  const { t } = useTranslation()
  const grid = buildRuntimeGrid(runtimes)

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Cpu className="size-4 text-muted-foreground" />
          <h2 className="text-base font-semibold">{t('runtimeAssets.jdkRegion')}</h2>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">
            {syncedAt
              ? t('runtimeAssets.lastSync', { time: formatRelativeTime(syncedAt) })
              : t('runtimeAssets.neverSynced')}
          </span>
          <Button variant="outline" size="sm" disabled={refreshing} onClick={onRefresh}>
            <RefreshCw className={cn('size-4', refreshing && 'animate-spin')} />
            {t('runtimeAssets.refresh')}
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <StatCard label={t('runtimeAssets.nodeCount')} value={summary.nodeCount} />
        <StatCard label={t('runtimeAssets.jdkCount')} value={summary.jdkCount} />
        <StatCard label={t('runtimeAssets.referencedJdk')} value={summary.referencedJdk} />
        <StatCard label={t('runtimeAssets.instanceRefs')} value={summary.instanceRefs} accent />
      </div>

      {runtimes.length === 0 ? (
        <Panel>
          <EmptyState icon={<Coffee />} title={t('runtimeAssets.jdkEmpty')} />
        </Panel>
      ) : (
        /* 可视化：节点×运行时引用矩阵（FR-301 多类型）——列=类型徽章+版本，格内数字=引用实例数（非 JDK 恒 0）。 */
        <Panel title={t('runtimeAssets.runtimeMatrixTitle')} bodyClassName="p-0">
          <Table className="text-xs">
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead className="sticky left-0 z-10 border-r bg-muted/50">{t('runtimeAssets.node')}</TableHead>
                {grid.columns.map((col) => (
                  <TableHead key={col.key} className="text-center">
                    <span
                      className={cn(
                        'rounded px-1.5 py-0.5 text-[9px] font-medium',
                        col.type === 'jdk'
                          ? 'bg-status-info/15 text-status-info'
                          : 'bg-status-success/15 text-status-success',
                      )}
                    >
                      {RUNTIME_TYPE_LABEL[col.type] ?? col.type}
                    </span>
                    <div className="mt-0.5 font-mono text-[10px] text-muted-foreground">
                      {col.type === 'jdk' ? `${col.label} ${col.majorVersion}` : `v${col.majorVersion}`}
                    </div>
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {grid.rows.map((row) => (
                <TableRow key={row.nodeId}>
                  <TableCell className="sticky left-0 z-10 border-r bg-card whitespace-nowrap">
                    <span className="inline-flex items-center gap-1.5">
                      <span
                        className={cn(
                          'size-1.5 rounded-full',
                          row.nodeOnline ? 'bg-status-success' : 'bg-muted-foreground',
                        )}
                      />
                      {row.nodeName || `#${row.nodeId}`}
                    </span>
                  </TableCell>
                  {grid.columns.map((col) => {
                    const cell = row.cells[col.key]
                    if (!cell) {
                      return (
                        <TableCell key={col.key} className="text-center text-muted-foreground/40">·</TableCell>
                      )
                    }
                    // 引用越多色越深（冷热可视）：0=灰，>0=主色调深浅。
                    const hot = cell.refCount > 0
                    return (
                      <TableCell key={col.key} className="text-center">
                        <span
                          className={cn(
                            'inline-flex min-w-7 items-center justify-center rounded px-1.5 py-0.5 font-mono',
                            hot ? 'bg-primary/15 font-semibold text-primary' : 'bg-muted text-muted-foreground',
                          )}
                          title={cell.items.map((it) => `${it.version || it.name} · ${it.refCount}`).join('\n')}
                        >
                          {cell.refCount}
                        </span>
                      </TableCell>
                    )
                  })}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}

      {/* 明细：每个 JDK + 其引用实例（引用关系下钻 + 删除占用方提示）。 */}
      {jdks.length > 0 && (
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          {jdks.map((j) => (
            <JDKCard key={j.id} jdk={j} onDeleteJdk={onDeleteJdk} deleting={deletingJdkId === j.id} />
          ))}
        </div>
      )}
    </section>
  )
}

function JDKCard({
  jdk,
  onDeleteJdk,
  deleting,
}: {
  jdk: JDKMatrixItem
  onDeleteJdk: (target: RuntimeAssetsJDKRef) => void
  /** 本卡片对应的删除在途（按钮禁用；删除目标由容器从 mutation 变量折算）。 */
  deleting: boolean
}) {
  const { t } = useTranslation()
  const [confirming, setConfirming] = useState(false)

  const onDelete = () => {
    onDeleteJdk({ nodeId: jdk.nodeId, jdkId: jdk.id })
    setConfirming(false)
  }

  return (
    <Panel
      title={
        <span className="flex items-center gap-2">
          <span className="text-foreground">
            {jdk.vendor} {jdk.majorVersion}
          </span>
          <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] font-normal text-muted-foreground">
            {jdk.version || '—'}
          </span>
          {jdk.managed && (
            <span className="rounded bg-status-info/15 px-1.5 py-0.5 text-[10px] font-normal text-status-info">
              {t('runtimeAssets.managed')}
            </span>
          )}
        </span>
      }
      actions={
        <Button
          variant="ghost"
          size="icon-xs"
          className="text-muted-foreground hover:text-destructive"
          disabled={deleting}
          onClick={() => setConfirming(true)}
          aria-label={t('common.delete')}
        >
          <Trash2 />
        </Button>
      }
      bodyClassName="space-y-2 p-3"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="inline-flex items-center gap-1">
          <span
            className={cn('size-1.5 rounded-full', jdk.nodeOnline ? 'bg-status-success' : 'bg-muted-foreground')}
          />
          {jdk.nodeName || `#${jdk.nodeId}`}
        </span>
        <span>{jdk.arch || '—'}</span>
        <span className="font-mono">{jdk.refCount} {t('runtimeAssets.refs')}</span>
      </div>
      <div className="overflow-hidden text-ellipsis whitespace-nowrap rounded bg-muted p-1.5 font-mono text-[11px]">
        {jdk.path}
      </div>
      {jdk.instances.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {jdk.instances.map((inst) => (
            <span
              key={inst.id}
              className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]"
              title={`${inst.binding === 'direct' ? t('runtimeAssets.bindDirect') : t('runtimeAssets.bindMajor')} · ${inst.uuid}`}
            >
              <span className={cn('size-1.5 shrink-0 rounded-full', LEVEL_DOT[instanceStatusLevel(inst.status)])} />
              {inst.name}
              <span className="text-muted-foreground/70">
                {inst.binding === 'direct' ? t('runtimeAssets.bindDirectShort') : t('runtimeAssets.bindMajorShort')}
              </span>
            </span>
          ))}
        </div>
      ) : (
        <p className="text-[11px] text-muted-foreground">{t('runtimeAssets.noRefs')}</p>
      )}

      {/* 删除二次确认：与原调用点逐字一致（未声明 scope、未传 allowed）——门禁语义不因迁包改动。 */}
      <DangerConfirm
        open={confirming}
        title={jdk.managed
          ? t('nodes.jdkDeleteFilesTitle', '删除 JDK（含文件）?')
          : t('nodes.jdkDeleteRecordTitle', '删除 JDK 登记记录?')}
        description={jdk.managed
          ? t('nodes.jdkDeleteFilesDesc', '该 JDK 由平台下载托管，删除将一并移除 Worker 上的文件，不可恢复。请输入「厂商 主版本」确认。')
          : t('nodes.jdkDeleteRecordDesc', '外部登记的 JDK 仅删除平台记录，不影响磁盘上的 JDK 文件。')}
        confirmLabel={t('common.delete')}
        confirmText={jdk.managed ? `${jdk.vendor} ${jdk.majorVersion}` : undefined}
        onConfirm={onDelete}
        onCancel={() => setConfirming(false)}
      />
    </Panel>
  )
}

/* ============================ 制品区 ============================ */

function AssetSection({
  groups,
  summary,
  channels,
  onDeleteAsset,
  deletingAssetId,
  onImportAsset,
  importing,
  onBatchDeploy,
  deploying,
  deployResult,
  renderInstancePicker,
}: {
  groups: AssetTypeGroup[]
  channels: RuntimeAssetsChannelRefView[]
  summary: {
    assetCount: number
    totalSize: number
    referencedCount: number
    hotCount: number
    archivedCount: number
    externalCount: number
    lostCount: number
  }
  onDeleteAsset: (args: RuntimeAssetDeleteArgs) => void
  deletingAssetId: number | null
  onImportAsset: (args: RuntimeAssetImportArgs) => Promise<boolean>
  importing: boolean
  onBatchDeploy: (args: RuntimeAssetBatchDeployArgs) => void
  deploying: boolean
  deployResult: PluginBatchDeployResultView | null
  renderInstancePicker: (args: RuntimeAssetsInstancePickerArgs) => ReactNode
}) {
  const { t } = useTranslation()
  // 筛选是纯本地过滤（不触发取数），故草稿留视图内；搜索词不进查询键。
  const [filter, setFilter] = useState<AssetFilter>(DEFAULT_ASSET_FILTER)
  const [importOpen, setImportOpen] = useState(false)
  const filtered = filterAssetGroups(groups, filter)
  const pluginAssets = groups.find((g) => g.type === 'plugin')?.items ?? []

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Package className="size-4 text-muted-foreground" />
          <h2 className="text-base font-semibold">{t('runtimeAssets.assetRegion')}</h2>
        </div>
        <Button variant="outline" size="sm" onClick={() => setImportOpen(true)}>
          <Upload className="size-4" />
          {t('runtimeAssets.import')}
        </Button>
      </div>
      {importOpen && (
        <ImportAssetDialog
          open={importOpen}
          onOpenChange={setImportOpen}
          onImportAsset={onImportAsset}
          importing={importing}
        />
      )}

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <StatCard label={t('runtimeAssets.assetCount')} value={summary.assetCount} />
        <StatCard label={t('runtimeAssets.totalSize')} value={formatBytes(summary.totalSize)} accent />
        <StatCard label={t('runtimeAssets.referencedAssets')} value={summary.referencedCount} />
        <StatCard
          label={t('runtimeAssets.hotCold')}
          value={
            <span className="text-sm">
              {summary.hotCount}
              <span className="text-muted-foreground"> / {summary.archivedCount + summary.externalCount}</span>
            </span>
          }
        />
      </div>

      {/* 筛选：类型 + 仅被引用 + 搜索（按实例/类型筛选的「类型」维度 + 内容搜索）。 */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex flex-wrap gap-1">
          {ASSET_TYPES.map((ty) => (
            <button
              key={ty}
              type="button"
              onClick={() => setFilter((f) => ({ ...f, type: ty }))}
              className={cn(
                'rounded px-2 py-0.5 text-xs transition-colors',
                filter.type === ty
                  ? 'bg-primary text-primary-foreground'
                  : 'bg-muted text-muted-foreground hover:bg-accent',
              )}
            >
              {ty === 'all' ? t('runtimeAssets.typeAll') : ty}
            </button>
          ))}
        </div>
        <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={filter.onlyReferenced}
            onChange={(e) => setFilter((f) => ({ ...f, onlyReferenced: e.target.checked }))}
          />
          {t('runtimeAssets.onlyReferenced')}
        </label>
        <Input
          value={filter.search}
          onChange={(e) => setFilter((f) => ({ ...f, search: e.target.value }))}
          placeholder={t('runtimeAssets.searchPlaceholder')}
          className="h-8 w-48 text-xs"
        />
      </div>

      {filtered.length === 0 ? (
        <Panel>
          {/* 区分「库为空」与「筛选无命中」：后者用 SearchX 提示与筛选条件相关。 */}
          {summary.assetCount === 0 ? (
            <EmptyState icon={<Package />} title={t('runtimeAssets.assetEmpty')} />
          ) : (
            <EmptyState icon={<SearchX />} title={t('runtimeAssets.noMatch')} />
          )}
        </Panel>
      ) : (
        filtered.map((g) => (
          <Panel
            key={g.type}
            title={
              <span className="flex items-center gap-2">
                <span className="font-mono text-foreground">{g.type}</span>
                <span className="font-normal text-muted-foreground">
                  {g.items.length} · {formatBytes(g.totalSize)}
                </span>
              </span>
            }
            bodyClassName="p-0"
          >
            <Table className="text-xs">
              <TableHeader className="bg-muted/40">
                <TableRow>
                  <TableHead>{t('runtimeAssets.name')}</TableHead>
                  <TableHead>{t('runtimeAssets.version')}</TableHead>
                  <TableHead>{t('runtimeAssets.sha256')}</TableHead>
                  <TableHead className="text-right">{t('runtimeAssets.size')}</TableHead>
                  <TableHead className="text-center">{t('runtimeAssets.storage')}</TableHead>
                  {g.type === 'client-file' && <TableHead>{t('runtimeAssets.storageLocation')}</TableHead>}
                  {g.type === 'client-file' && <TableHead className="text-center">{t('runtimeAssets.reconcileState')}</TableHead>}
                  <TableHead className="text-center">{t('runtimeAssets.refs')}</TableHead>
                  <TableHead className="text-right">{t('common.actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {g.items.map((a) => (
                  <AssetRow
                    key={a.id}
                    asset={a}
                    pluginAssets={pluginAssets}
                    channels={channels}
                    onDeleteAsset={onDeleteAsset}
                    deleting={deletingAssetId === a.id}
                    onBatchDeploy={onBatchDeploy}
                    deploying={deploying}
                    deployResult={deployResult}
                    renderInstancePicker={renderInstancePicker}
                  />
                ))}
              </TableBody>
            </Table>
          </Panel>
        ))
      )}
    </section>
  )
}

/**
 * 导入制品弹窗（FR-155：补齐制品导入下载进度）。
 * 选类型 + 选本地文件后由容器走 multipart 入库（POST /assets），上传期间显示实时进度条，
 * 进度由容器回调驱动（与插件上传同一机制）。成功后容器失效 overview，新制品即刻出现。
 * 遵循模态纪律（ui-modals）：内容自适应壳 + 内部滚动，头/脚固定。
 */
function ImportAssetDialog({
  open,
  onOpenChange,
  onImportAsset,
  importing,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onImportAsset: (args: RuntimeAssetImportArgs) => Promise<boolean>
  importing: boolean
}) {
  const { t } = useTranslation()
  const [type, setType] = useState<AssetType>('core')
  const [name, setName] = useState('')
  const [version, setVersion] = useState('')
  const [file, setFile] = useState<File | null>(null)
  // 已上传/总字节；null=尚未开始上传（不渲染进度条）。纯展示状态，故留视图内。
  const [progress, setProgress] = useState<{ loaded: number; total: number } | null>(null)

  const submit = () => {
    if (!file) return
    // 先同步置 0%：上传期间（含 mock 的瞬时请求）进度区即刻可见，与迁包前一致。
    setProgress({ loaded: 0, total: file.size })
    void onImportAsset({
      type,
      file,
      name: name.trim() || undefined,
      version: version.trim() || undefined,
      onProgress: (loaded, total) => setProgress({ loaded, total: total || file.size }),
      onSettled: () => setProgress(null),
    }).then((ok) => {
      // 成功关窗；失败保留草稿与进度收起后的表单，便于改参数重试。
      if (ok) onOpenChange(false)
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('runtimeAssets.importTitle')}</DialogTitle>
          <DialogDescription>{t('runtimeAssets.importDescription')}</DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-3">
          <label className="block space-y-1 text-sm">
            <span className="font-medium">{t('runtimeAssets.importType')}</span>
            <select
              value={type}
              onChange={(e) => setType(e.target.value as AssetType)}
              disabled={importing}
              className="h-9 w-full rounded-md border bg-background px-2 text-sm"
            >
              {IMPORTABLE_ASSET_TYPES.map((ty) => (
                <option key={ty} value={ty}>{ty}</option>
              ))}
            </select>
          </label>
          <label className="block space-y-1 text-sm">
            <span className="font-medium">{t('runtimeAssets.importFile')}</span>
            <input
              type="file"
              aria-label={t('runtimeAssets.importFile')}
              disabled={importing}
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
              className="block w-full text-sm text-muted-foreground file:mr-3 file:rounded-md file:border file:bg-muted file:px-3 file:py-1.5 file:text-sm"
            />
          </label>
          <div className="grid grid-cols-2 gap-2">
            <label className="block space-y-1 text-sm">
              <span className="font-medium">{t('runtimeAssets.importName')}</span>
              <Input value={name} onChange={(e) => setName(e.target.value)} disabled={importing} className="h-9" />
            </label>
            <label className="block space-y-1 text-sm">
              <span className="font-medium">{t('runtimeAssets.importVersion')}</span>
              <Input value={version} onChange={(e) => setVersion(e.target.value)} disabled={importing} className="h-9" />
            </label>
          </div>
          {progress && (
            <div role="status" aria-label={t('runtimeAssets.importProgressLabel')}>
              <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
                <span className="truncate">{file?.name}</span>
                <span className="font-mono tabular-nums">{importPercent(progress.loaded, progress.total)}%</span>
              </div>
              <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                <div
                  className="h-full rounded-full bg-primary transition-[width]"
                  style={{ width: `${importPercent(progress.loaded, progress.total)}%` }}
                />
              </div>
            </div>
          )}
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={importing}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} disabled={!file || importing}>
            {importing ? t('runtimeAssets.importing') : t('runtimeAssets.import')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function AssetRow({
  asset,
  pluginAssets,
  channels,
  onDeleteAsset,
  deleting,
  onBatchDeploy,
  deploying,
  deployResult,
  renderInstancePicker,
}: {
  asset: AssetInfo
  pluginAssets: AssetInfo[]
  channels: RuntimeAssetsChannelRefView[]
  onDeleteAsset: (args: RuntimeAssetDeleteArgs) => void
  /** 本行删除在途（按钮禁用）。 */
  deleting: boolean
  onBatchDeploy: (args: RuntimeAssetBatchDeployArgs) => void
  deploying: boolean
  deployResult: PluginBatchDeployResultView | null
  renderInstancePicker: (args: RuntimeAssetsInstancePickerArgs) => ReactNode
}) {
  const { t } = useTranslation()
  const [confirming, setConfirming] = useState(false)
  const [deployOpen, setDeployOpen] = useState(false)
  const referenced = asset.refCount > 0

  const onDelete = () => {
    onDeleteAsset({ assetId: asset.id, refCount: asset.refCount })
    setConfirming(false)
  }

  const storageLabel =
    asset.storageState === 'lost'
      ? t('runtimeAssets.storageLost')
      : asset.storageState === 'archived'
        ? t('runtimeAssets.storageArchived')
        : asset.storageState === 'external'
          ? t('runtimeAssets.storageExternal')
          : t('runtimeAssets.storageHot')
  const channelName = channels.find((channel) => channel.id === asset.storageChannelId)?.name
  const storageLocation = asset.storageBackend === 's3'
    ? channelName || t('runtimeAssets.storageChannelFallback', { id: asset.storageChannelId })
    : t('runtimeAssets.storageLocal')
  const metadata = parseAssetMetadata(asset.metadata)
  const clientFileInfo = asset.type === 'client-file'
    ? [asset.filename, metadata.path, metadata.codec].filter(Boolean).join(' · ')
    : ''

  return (
    <TableRow>
      <TableCell>
        <div>{asset.name || asset.filename || '—'}</div>
        {clientFileInfo && <div className="mt-0.5 max-w-64 truncate text-[11px] text-muted-foreground" title={clientFileInfo}>{clientFileInfo}</div>}
      </TableCell>
      <TableCell className="text-muted-foreground">{asset.version || '—'}</TableCell>
      <TableCell className="font-mono text-[11px] text-muted-foreground" title={asset.sha256}>
        {shortSha(asset.sha256)}
      </TableCell>
      <TableCell className="text-right tabular-nums">{formatBytes(asset.size)}</TableCell>
      <TableCell className="text-center">
        <span
          className={cn(
            'rounded px-1.5 py-0.5 text-[10px]',
            asset.storageState === 'lost'
              ? 'bg-destructive/15 text-destructive'
              : asset.storageState === 'hot'
                ? 'bg-status-success/15 text-status-success'
                : 'bg-muted text-muted-foreground',
          )}
        >
          {storageLabel}
        </span>
      </TableCell>
      {asset.type === 'client-file' && <TableCell>{storageLocation}</TableCell>}
      {asset.type === 'client-file' && (
        <TableCell className="text-center">
          <span
            className={cn(
              'rounded px-1.5 py-0.5 text-[10px]',
              asset.storageState === 'lost'
                ? 'bg-destructive/15 text-destructive'
                : 'bg-status-success/15 text-status-success',
            )}
            title={asset.storageState === 'lost' ? t('runtimeAssets.reconcileLostHint') : undefined}
          >
            {asset.storageState === 'lost' ? t('runtimeAssets.reconcileLost') : t('runtimeAssets.reconcileOk')}
          </span>
        </TableCell>
      )}
      <TableCell className="text-center">
        <span
          className={cn(
            'inline-flex min-w-6 justify-center rounded px-1.5 py-0.5 font-mono text-[11px]',
            referenced ? 'bg-primary/15 font-semibold text-primary' : 'bg-muted text-muted-foreground',
          )}
          title={referenced ? t('runtimeAssets.assetRefHint', { count: asset.refCount }) : t('runtimeAssets.noRefs')}
        >
          {asset.refCount}
        </span>
      </TableCell>
      <TableCell className="text-right">
        <div className="flex justify-end gap-1">
          {asset.type === 'plugin' && (
            <Button variant="outline" size="xs" onClick={() => setDeployOpen(true)}>
              {t('plugins.batchDeploy.action')}
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon-xs"
            className="text-muted-foreground hover:text-destructive"
            disabled={referenced || deleting}
            onClick={() => setConfirming(true)}
            aria-label={t('common.delete')}
            title={referenced ? t('runtimeAssets.assetDeleteReferenced', { count: asset.refCount }) : undefined}
          >
            <Trash2 />
          </Button>
        </div>
        {asset.type === 'plugin' && deployOpen && (
          <PluginBatchDeployDialog
            open={deployOpen}
            initialAsset={asset}
            pluginAssets={pluginAssets}
            onOpenChange={setDeployOpen}
            onBatchDeploy={onBatchDeploy}
            deploying={deploying}
            deployResult={deployResult}
            renderInstancePicker={renderInstancePicker}
          />
        )}
        {/* 删除二次确认：与原调用点逐字一致（未声明 scope、未传 allowed）——门禁语义不因迁包改动。 */}
        <DangerConfirm
          open={confirming}
          title={t('runtimeAssets.assetDeleteConfirm', { name: asset.name || asset.filename })}
          description={
            referenced
              ? t('runtimeAssets.assetDeleteReferenced', { count: asset.refCount })
              : t('runtimeAssets.assetDeleteDescription')
          }
          confirmLabel={t('common.delete')}
          onConfirm={onDelete}
          onCancel={() => setConfirming(false)}
        />
      </TableCell>
    </TableRow>
  )
}

function PluginBatchDeployDialog({
  open,
  initialAsset,
  pluginAssets,
  onOpenChange,
  onBatchDeploy,
  deploying,
  deployResult,
  renderInstancePicker,
}: {
  open: boolean
  initialAsset: AssetInfo
  pluginAssets: AssetInfo[]
  onOpenChange: (open: boolean) => void
  onBatchDeploy: (args: RuntimeAssetBatchDeployArgs) => void
  deploying: boolean
  deployResult: PluginBatchDeployResultView | null
  renderInstancePicker: (args: RuntimeAssetsInstancePickerArgs) => ReactNode
}) {
  const { t } = useTranslation()
  // 勾选集合是纯 UI 状态：与「部署到 N 个实例」计数同源，故留本组件（提交仍交容器）。
  const [selectedAssetIds, setSelectedAssetIds] = useState<number[]>([initialAsset.id])
  const [selectedInstanceIds, setSelectedInstanceIds] = useState<number[]>([])
  const [confirmOpen, setConfirmOpen] = useState(false)

  const toggleAsset = (id: number, checked: boolean) => {
    setSelectedAssetIds((ids) => (checked ? [...new Set([...ids, id])] : ids.filter((item) => item !== id)))
  }
  const toggleInstance = (id: number, checked: boolean) => {
    setSelectedInstanceIds((ids) => (checked ? [...new Set([...ids, id])] : ids.filter((item) => item !== id)))
  }
  const submit = () => {
    if (selectedAssetIds.length === 0 || selectedInstanceIds.length === 0) return
    setConfirmOpen(true)
  }
  const confirmDeploy = () => {
    onBatchDeploy({ assetIds: selectedAssetIds, target: { ids: selectedInstanceIds } })
    setConfirmOpen(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-3xl`}>
        <DialogHeader>
          <DialogTitle>{t('plugins.batchDeploy.title')}</DialogTitle>
          <DialogDescription>{t('plugins.batchDeploy.description')}</DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="grid gap-4 py-2 md:grid-cols-2">
          <section className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-medium">{t('plugins.batchDeploy.assets')}</h3>
              <span className="text-xs text-muted-foreground">
                {t('plugins.batchDeploy.selectedAssets', { count: selectedAssetIds.length })}
              </span>
            </div>
            <div className="space-y-2 rounded border p-2">
              {pluginAssets.map((asset) => (
                <label key={asset.id} className="flex cursor-pointer items-start gap-2 rounded p-2 hover:bg-muted/60">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={selectedAssetIds.includes(asset.id)}
                    onChange={(e) => toggleAsset(asset.id, e.target.checked)}
                  />
                  <span className="min-w-0 text-sm">
                    <span className="block font-medium">{asset.name || asset.filename}</span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {asset.filename} · {formatBytes(asset.size)}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </section>

          <section className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-medium">{t('plugins.batchDeploy.targets')}</h3>
              <span className="text-xs text-muted-foreground">
                {t('plugins.batchDeploy.selectedTargets', { count: selectedInstanceIds.length })}
              </span>
            </div>
            {/* 实例候选经插槽注入：候选窗口与关键字状态由容器按服务端搜索结果提供（千级实例）。 */}
            {renderInstancePicker({ selected: selectedInstanceIds, onToggle: toggleInstance })}
          </section>
          {confirmOpen && (
            <div
              role="alert"
              className="rounded border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive md:col-span-2"
            >
              <div className="font-medium">{t('plugins.batchDeploy.confirmTitle')}</div>
              <p className="mt-1 text-xs">
                {t('plugins.batchDeploy.confirmDescription', {
                  assetCount: selectedAssetIds.length,
                  instanceCount: selectedInstanceIds.length,
                })}
              </p>
            </div>
          )}
          {deployResult && <PluginBatchDeployResultPanel result={deployResult} />}
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            variant={confirmOpen ? 'destructive' : 'default'}
            onClick={confirmOpen ? confirmDeploy : submit}
            disabled={deploying || selectedAssetIds.length === 0 || selectedInstanceIds.length === 0}
          >
            {deploying
              ? t('plugins.batchDeploy.submitting')
              : confirmOpen
                ? t('plugins.batchDeploy.confirmLabel')
                : t('plugins.batchDeploy.submit', { count: selectedInstanceIds.length })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function PluginBatchDeployResultPanel({ result }: { result: PluginBatchDeployResultView }) {
  const { t } = useTranslation()
  return (
    <div role="status" className="rounded border border-status-success/30 bg-status-success/10 p-3 text-xs md:col-span-2">
      <div className="font-medium text-status-success">
        {t('plugins.batchDeploy.result', { success: result.success, skipped: result.skipped, failed: result.failed })}
      </div>
      <div className="mt-1 text-muted-foreground">
        {result.results.map((item) => (
          <span key={item.id} className="mr-3 inline-block">
            {item.name}: {item.error || item.reason || t('plugins.batchDeploy.ok')}
          </span>
        ))}
      </div>
    </div>
  )
}

export default RuntimeAssetsPageView
