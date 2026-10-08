/**
 * @file SystemUpdatePageView：面板自更新页（`/system-update`）的受控视图——检查结果 / 全网升级进度 /
 *       Worker 二进制缓存三份查询与检查、CP 升级回滚、节点升级回滚、全网升级、预缓存这些写动作
 *       全由应用容器注入；视图只保留弹窗开合与金丝雀草稿这类纯 UI 状态。
 * @input lib/artifact-cache（formatCacheBytes）、lib/relative-time（formatRelativeTime）、lib/clipboard（copyToClipboard）、
 *        views/DangerConfirm（危险操作二次确认）、views/ReleaseNotes（更新说明 Markdown）、
 *        layout（PageShell / PageHeader）、Badge / Button / Checkbox / Dialog / Input / Label / Table 原语、翻译上下文
 * @output SystemUpdatePageView、SystemUpdatePageViewProps、SystemUpdateControlPlaneCard、SystemUpdateControlPlaneCardProps、
 *         SystemUpdateNodesSection、SystemUpdateNodesSectionProps、SystemUpdateWorkerAssetsPanel、
 *         SystemUpdateWorkerAssetsPanelProps、SystemUpdateWorkerAssetTableRow、SystemUpdateWorkerAssetTableRowProps、
 *         SystemUpdateWorkerAssetCacheBadge、SystemUpdateWorkerAssetCacheBadgeProps、SystemUpdateNodeRow、
 *         SystemUpdateNodeRowProps、SystemUpdateVersionBadge、SystemUpdateVersionBadgeProps、SystemUpdateRolloutPanel、
 *         SystemUpdateRolloutPanelProps、SystemUpdateRolloutPhaseBadge、SystemUpdateRolloutPhaseBadgeProps、
 *         SystemUpdateRolloutStateBadge、SystemUpdateRolloutStateBadgeProps、SystemUpdateRolloutNodeBadge、
 *         SystemUpdateRolloutNodeBadgeProps、SystemUpdateComponentStatus、SystemUpdateWorkerAssetEntry、
 *         SystemUpdateWorkerAssetRow、SystemUpdateWorkerAssetTarget、SystemUpdateNodePending、SystemUpdateRolloutDraft、
 *         SystemUpdateRollout、SystemUpdateRolloutNode、SystemUpdateCheckResult
 * @sync apps/control-plane-web/src/pages/SystemUpdatePage.tsx、apps/control-plane-web/src/pages/SystemUpdatePage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-081 面板自更新、FR-059 危险确认门禁、FR-110 未配源版本回显、
 *        FR-155 金丝雀分批、FR-182 备份回滚、FR-186 检查结果缓存、FR-190 Worker 二进制缓存）
 */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw, ArrowUpCircle, ArrowDownCircle, ServerCog, AlertCircle, CheckCircle2, Clock, Copy, Download } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import DangerConfirm from '@jianmanager/ui/components/views/DangerConfirm'
import { ReleaseNotes } from '@jianmanager/ui/components/views/ReleaseNotes'
import { formatCacheBytes } from '@jianmanager/ui/lib/artifact-cache'
import { copyToClipboard } from '@jianmanager/ui/lib/clipboard'
import { formatRelativeTime } from '@jianmanager/ui/lib/relative-time'

/**
 * 单个组件（CP 或某节点）的版本对比结果（本视图所需的最小结构）。
 *
 * 必填性照搬应用侧 `@/api/selfUpdate` 的 `ComponentStatus`：这些字段后端恒给（空值用空串），
 * 视图若声明得比它宽松，容器反传时会在「必填 vs 可选」上撞类型错。只省略本视图不读的字段。
 */
export interface SystemUpdateComponentStatus {
  /** 节点 ID；CP 自身为 0（字段省略）。 */
  nodeId?: number
  name?: string
  /** 节点是否在线（离线节点单点动作全部禁用）。 */
  online: boolean
  currentVersion: string
  os: string
  arch: string
  /** 更新源最新版与当前版不同且有匹配制品。 */
  updateAvailable: boolean
  /** 更新源中存在匹配该组件平台（component+os+arch）的制品。 */
  artifactAvailable: boolean
  /** 升级前备份的版本（FR-182）：非空时可一键「回滚 v{backupVersion}」。 */
  backupVersion?: string
}

/** Worker 二进制 CP 代理缓存条目（FR-190，本视图所需的最小结构）。 */
export interface SystemUpdateWorkerAssetEntry {
  version: string
  os: string
  arch: string
  /** CP 本地是否已缓存该平台制品。 */
  cached: boolean
  sha256: string
  /** 制品字节数（`formatCacheBytes` 展示）。 */
  size: number
  /** 缓存完成时刻（ISO 字符串）；空表示未缓存。 */
  cachedAt?: string
  /** 最近一次预缓存失败原因；空表示无错误。 */
  lastError?: string
}

/** Worker 缓存表格行：条目 + 稳定 key（`version/os/arch` 三元组）。 */
export type SystemUpdateWorkerAssetRow = SystemUpdateWorkerAssetEntry & { key: string }

/** 预缓存的目标平台（请求要素，与后端按 os+arch 定位制品的口径一致）。 */
export interface SystemUpdateWorkerAssetTarget {
  os: string
  arch: string
}

/** 单节点升级/回滚的在途目标；`null` 表示空闲。 */
export interface SystemUpdateNodePending {
  nodeId: number
  /** 在途动作：升级或回滚。 */
  action: 'upgrade' | 'rollback'
}

/** 全网升级策略草稿（FR-155 金丝雀分批）——输入框原样持有字符串，折算请求体由容器负责。 */
export interface SystemUpdateRolloutDraft {
  /** 金丝雀节点数输入框原文（空串=不设金丝雀）。 */
  canarySize: string
  /** 每批节点数输入框原文（空串=剩余一次全部）。 */
  batchSize: string
  /** 金丝雀失败即中止。 */
  abortOnCanaryFailure: boolean
}

/** rollout 中单个节点的升级状态。 */
export interface SystemUpdateRolloutNode {
  nodeId: number
  name: string
  /** pending | upgrading | succeeded | failed | skipped。 */
  state: string
  fromVersion: string
  toVersion: string
  error: string
}

/** 一次全网逐节点升级编排的进度快照。 */
export interface SystemUpdateRollout {
  targetVersion: string
  /** idle | running | completed。 */
  state: string
  total: number
  succeeded: number
  failed: number
  pending: number
  nodes: SystemUpdateRolloutNode[]
  /** 编排阶段（FR-155）：canary | rolling | completed | aborted。 */
  phase?: string
  /** 本次金丝雀节点数（FR-155）。 */
  canarySize?: number
  /** 本次剩余分批大小（FR-155，<=0=剩余全部一批）。 */
  batchSize?: number
  /** 当前批次（FR-155，1-based，金丝雀=第 1 批）。 */
  currentBatch?: number
}

/** `GET /self-update/check` 的结果（本视图所需的最小结构）。 */
export interface SystemUpdateCheckResult {
  /** 是否已配置更新源；未配源时不展示「最新版本对比」与更新说明，但版本回显照旧（FR-110）。 */
  configured: boolean
  latestVersion: string
  /** 更新说明（GitHub release body 的 markdown，FR-186）。 */
  notes: string
  /** 更新源标识（FR-175）：github:owner/repo@channel | feed | 空。 */
  source?: string
  controlPlane: SystemUpdateComponentStatus
  nodes: SystemUpdateComponentStatus[]
  /** 「上次成功检查」时刻（FR-186，ISO 字符串）；无缓存/未检查时省略。 */
  checkedAt?: string
}

/** 版本/平台三元组 → 行 key（与容器注入的在途目标按 os+arch 对齐）。 */
function workerAssetKey(version: string, os: string, arch: string) {
  return `${version}/${os}/${arch}`
}

/**
 * 按「当前 CP 版本」聚合 Worker 缓存行。
 *
 * 两步拼装的原因：制品库可能同时存着多个版本的缓存（旧版本的条目不能遮住当前版本的缺失），
 * 所以先只收该版本的条目，再按节点平台补齐缺的行（补出来的即「未缓存」）。
 * 排序按 key 字典序，保证同一批数据每次渲染顺序一致。
 */
function buildWorkerAssetRows(
  nodes: SystemUpdateComponentStatus[],
  assets: SystemUpdateWorkerAssetEntry[],
  version: string,
): SystemUpdateWorkerAssetRow[] {
  const rows = new Map<string, SystemUpdateWorkerAssetRow>()
  for (const asset of assets) {
    if (asset.version === version) {
      rows.set(workerAssetKey(asset.version, asset.os, asset.arch), {
        ...asset,
        key: workerAssetKey(asset.version, asset.os, asset.arch),
      })
    }
  }
  for (const node of nodes) {
    const key = workerAssetKey(version, node.os, node.arch)
    if (!rows.has(key)) {
      rows.set(key, { key, version, os: node.os, arch: node.arch, cached: false, sha256: '', size: 0, cachedAt: '', lastError: '' })
    }
  }
  return Array.from(rows.values()).sort((a, b) => a.key.localeCompare(b.key))
}

/** 缓存时间戳 → 表格文案：把 ISO 的 `T` 与 `Z` 换成可读形式；缺省回 '-'。 */
function formatWorkerAssetTime(value?: string) {
  return value ? value.replace('T', ' ').replace(/(\.\d+)?Z$/, ' UTC') : '-'
}

/** 版本对比徽章属性。 */
export interface SystemUpdateVersionBadgeProps {
  /** 当前版本。 */
  current: string
  /** 更新源最新版本。 */
  latest: string
  /** 是否存在可升级版本。 */
  updateAvailable: boolean
  /** 是否有匹配该平台的制品。 */
  artifactAvailable: boolean
  /** 组件是否离线（离线优先于其余判据）。 */
  offline?: boolean
}

/** 版本对比徽章：离线 / 可升级 / 无制品 / 已最新 / 无法判定。 */
export function SystemUpdateVersionBadge({
  current,
  latest,
  updateAvailable,
  artifactAvailable,
  offline,
}: SystemUpdateVersionBadgeProps) {
  const { t } = useTranslation()
  if (offline) {
    return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.offline', '离线')}</Badge>
  }
  if (updateAvailable) {
    return <Badge variant="outline" className="border-amber-500/50 text-amber-600">{t('systemUpdate.updatable', '可升级 → {{v}}', { v: latest })}</Badge>
  }
  if (!artifactAvailable) {
    return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.noArtifact', '无匹配制品')}</Badge>
  }
  if (current && latest && current.replace(/^v/, '') === latest.replace(/^v/, '')) {
    return (
      <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">
        <CheckCircle2 className="size-3.5" /> {t('systemUpdate.upToDate', '已最新')}
      </Badge>
    )
  }
  return <Badge variant="outline" className="text-muted-foreground">-</Badge>
}

/** Worker 缓存命中状态徽章属性。 */
export interface SystemUpdateWorkerAssetCacheBadgeProps {
  /** 该平台制品是否已在 CP 本地缓存。 */
  cached: boolean
}

/** Worker 缓存命中状态徽章。 */
export function SystemUpdateWorkerAssetCacheBadge({ cached }: SystemUpdateWorkerAssetCacheBadgeProps) {
  const { t } = useTranslation()
  if (cached) {
    return <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">{t('systemUpdate.workerAssetCachedState', '已缓存')}</Badge>
  }
  return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.workerAssetMissingState', '未缓存')}</Badge>
}

/** 单个平台的 Worker 缓存状态行属性。 */
export interface SystemUpdateWorkerAssetTableRowProps {
  /** 本行数据（条目 + 稳定 key）。 */
  row: SystemUpdateWorkerAssetRow
  /** 本行预缓存在途（按钮转圈并禁用）。 */
  pending: boolean
  /** 预缓存上报（容器执行 mutation + toast）。 */
  onCache: (target: SystemUpdateWorkerAssetTarget) => void
  /** 提示通道：复制回执文案由本视图算好，交给容器弹 toast（包内不引 sonner）。 */
  onNotify?: (kind: 'success' | 'error', message: string) => void
}

/** 单个平台的 Worker 缓存状态行。 */
export function SystemUpdateWorkerAssetTableRow({
  row,
  pending,
  onCache,
  onNotify,
}: SystemUpdateWorkerAssetTableRowProps) {
  const { t } = useTranslation()

  return (
    <TableRow>
      <TableCell className="whitespace-nowrap font-mono text-xs">{row.os}/{row.arch}</TableCell>
      <TableCell className="whitespace-nowrap font-mono text-xs">{row.version || '-'}</TableCell>
      <TableCell><SystemUpdateWorkerAssetCacheBadge cached={row.cached} /></TableCell>
      {/* 全量 64 位 hash 铺开会把表格挤宽；截断展示（前 8…后 8），title 悬停看全量、点击复制全量。 */}
      <TableCell className="whitespace-nowrap font-mono text-[11px]">
        {row.sha256 ? (
          <button
            type="button"
            className="inline-flex items-center gap-1 text-muted-foreground transition-colors hover:text-foreground"
            title={row.sha256}
            aria-label={t('systemUpdate.copySha', '复制 SHA256')}
            onClick={async () => {
              const ok = await copyToClipboard(row.sha256)
              // 成功/失败都必须有回执：HTTP 非安全上下文下连 execCommand 兜底也可能失败。
              onNotify?.(ok ? 'success' : 'error', ok ? t('artifactCache.shaCopied', '已复制 SHA256') : t('common.copyFailed', '复制失败'))
            }}
          >
            <span>{`${row.sha256.slice(0, 8)}…${row.sha256.slice(-8)}`}</span>
            <Copy className="size-3" />
          </button>
        ) : (
          '-'
        )}
      </TableCell>
      <TableCell className="whitespace-nowrap font-mono text-xs">{formatCacheBytes(row.size)}</TableCell>
      <TableCell className="whitespace-nowrap font-mono text-xs">{formatWorkerAssetTime(row.cachedAt)}</TableCell>
      <TableCell className={row.lastError ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
        {row.lastError || '-'}
      </TableCell>
      <TableCell className="text-right">
        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-2"
          disabled={pending}
          onClick={() => onCache({ os: row.os, arch: row.arch })}
        >
          {pending ? <RefreshCw className="size-4 animate-spin" /> : <Download className="size-4" />}
          {t('systemUpdate.cacheWorkerAsset', '预缓存')}
        </Button>
      </TableCell>
    </TableRow>
  )
}

/** Worker 二进制缓存面板属性。 */
export interface SystemUpdateWorkerAssetsPanelProps {
  /** 节点列表（用于按平台补齐「未缓存」行）。 */
  nodes: SystemUpdateComponentStatus[]
  /** 缓存行的版本口径（CP 当前版本，缺省回落更新源最新版）。 */
  version: string
  /** 缓存条目；容器取数后注入（缺省即无条目，仅按节点平台补齐）。 */
  assets?: SystemUpdateWorkerAssetEntry[]
  /** 取数中（标题栏转圈）。 */
  assetsFetching: boolean
  /** 取数失败的后端消息（缺省=无错误）。 */
  assetsErrorMessage?: string
  /** 预缓存在途的目标平台；null=空闲。 */
  pendingAsset: SystemUpdateWorkerAssetTarget | null
  /** 预缓存上报。 */
  onCacheAsset: (target: SystemUpdateWorkerAssetTarget) => void
  /** 提示通道（复制回执）。 */
  onNotify?: (kind: 'success' | 'error', message: string) => void
}

/** Worker 二进制 CP 代理缓存状态：按节点平台展示，并允许手动预缓存。 */
export function SystemUpdateWorkerAssetsPanel({
  nodes,
  version,
  assets,
  assetsFetching,
  assetsErrorMessage,
  pendingAsset,
  onCacheAsset,
  onNotify,
}: SystemUpdateWorkerAssetsPanelProps) {
  const { t } = useTranslation()
  const rows = buildWorkerAssetRows(nodes, assets ?? [], version)
  const title = t('systemUpdate.workerAssetsTitle', 'Worker 二进制缓存')

  return (
    <section role="region" aria-label={title} className="rounded-lg border">
      <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
        <div>
          <h3 className="text-sm font-semibold">{title}</h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t('systemUpdate.workerAssetsSubtitle', 'CP 本地缓存状态，按节点平台聚合。')}
          </p>
        </div>
        {assetsFetching && <RefreshCw className="size-4 animate-spin text-muted-foreground" />}
      </div>

      {assetsErrorMessage && (
        <div className="mx-4 mt-3 flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          <span>{assetsErrorMessage}</span>
        </div>
      )}

      <div className="overflow-x-auto">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('systemUpdate.platform', '平台')}</TableHead>
              <TableHead>{t('systemUpdate.version', '版本')}</TableHead>
              <TableHead>{t('systemUpdate.cacheState', '缓存')}</TableHead>
              <TableHead>sha256</TableHead>
              <TableHead>{t('systemUpdate.size', '大小')}</TableHead>
              <TableHead>{t('systemUpdate.cachedAt', '缓存时间')}</TableHead>
              <TableHead>{t('systemUpdate.lastError', '最近错误')}</TableHead>
              <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <SystemUpdateWorkerAssetTableRow
                key={row.key}
                row={row}
                pending={pendingAsset?.os === row.os && pendingAsset?.arch === row.arch}
                onCache={onCacheAsset}
                onNotify={onNotify}
              />
            ))}
            {rows.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="h-14 text-center text-muted-foreground">
                  {t('systemUpdate.workerAssetsEmpty', '暂无节点平台')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </section>
  )
}

/** 单个节点行属性。 */
export interface SystemUpdateNodeRowProps {
  /** 节点版本对比结果。 */
  node: SystemUpdateComponentStatus
  /** 更新源最新版本（升级确认文案用）。 */
  latest: string
  /** 全网升级在途：禁用本行的单点动作（原 `rolloutRunning`）。 */
  rolloutRunning: boolean
  /** 本行在途的动作；null=空闲。 */
  pending: 'upgrade' | 'rollback' | null
  /** DangerConfirm 的平台范围门禁结果（应用侧读登录态角色后注入）。 */
  dangerAllowed: boolean
  /** 单节点升级上报。 */
  onUpgrade: (nodeId: number) => void
  /** 单节点回滚上报。 */
  onRollback: (nodeId: number) => void
}

/** 单个节点行：版本对比 + 升级 + 回滚（有备份时）。 */
export function SystemUpdateNodeRow({
  node,
  latest,
  rolloutRunning,
  pending,
  dangerAllowed,
  onUpgrade,
  onRollback,
}: SystemUpdateNodeRowProps) {
  const { t } = useTranslation()
  // 两个确认框的开合是纯 UI 状态，留本行；在途判定由容器注入（`pending`）。
  const [confirm, setConfirm] = useState(false)
  const [confirmRollback, setConfirmRollback] = useState(false)
  const hasBackup = !!node.backupVersion

  return (
    <TableRow>
      <TableCell className="font-medium">{node.name}</TableCell>
      <TableCell>
        {node.online ? (
          <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">{t('systemUpdate.online', '在线')}</Badge>
        ) : (
          <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.offline', '离线')}</Badge>
        )}
      </TableCell>
      <TableCell className="font-mono text-xs">{node.os}/{node.arch}</TableCell>
      <TableCell className="font-mono text-xs">{node.currentVersion || '-'}</TableCell>
      <TableCell>
        <SystemUpdateVersionBadge current={node.currentVersion} latest={latest} updateAvailable={node.updateAvailable} artifactAvailable={node.artifactAvailable} offline={!node.online} />
      </TableCell>
      <TableCell className="text-right">
        <div className="flex items-center justify-end gap-1">
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2"
            disabled={!node.online || !hasBackup || pending === 'rollback' || rolloutRunning}
            onClick={() => setConfirmRollback(true)}
            title={hasBackup ? undefined : t('systemUpdate.noBackup', '无可回滚的备份')}
          >
            <ArrowDownCircle className="size-4" />
            {hasBackup
              ? t('systemUpdate.rollbackTo', '回滚 v{{v}}', { v: node.backupVersion })
              : t('systemUpdate.rollback', '回滚')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2"
            disabled={!node.updateAvailable || pending === 'upgrade' || rolloutRunning}
            onClick={() => setConfirm(true)}
          >
            <ArrowUpCircle className="size-4" />
            {t('systemUpdate.upgrade', '升级')}
          </Button>
        </div>

        {/* 危险动作边界（FR-059，逐字保持）：scope=platform、无 confirmText、不在途标记；
            门禁结果 allowed 由容器注入，本视图不得自行判定或放宽。 */}
        <DangerConfirm
          open={confirm}
          title={t('systemUpdate.nodeUpgradeConfirm', '确定升级该节点？')}
          description={t('systemUpdate.nodeUpgradeConfirmDesc', '将令该节点下载新版 Worker（{{v}}）、sha256 校验后替换并重启。daemon 模式下运行中的游戏服不掉。', { v: latest })}
          scope="platform"
          allowed={dangerAllowed}
          confirmLabel={t('systemUpdate.upgrade', '升级')}
          onConfirm={() => {
            setConfirm(false)
            if (node.nodeId === undefined) return
            onUpgrade(node.nodeId)
          }}
          onCancel={() => setConfirm(false)}
        />

        <DangerConfirm
          open={confirmRollback}
          title={t('systemUpdate.nodeRollbackConfirm', '确定回滚该节点？')}
          description={t('systemUpdate.nodeRollbackConfirmDesc', '将令该节点换回升级前备份（v{{v}}）、sha256 校验后替换并重启 Worker。daemon 模式下运行中的游戏服不掉。', { v: node.backupVersion })}
          scope="platform"
          allowed={dangerAllowed}
          confirmLabel={t('systemUpdate.rollback', '回滚')}
          onConfirm={() => {
            setConfirmRollback(false)
            if (node.nodeId === undefined) return
            onRollback(node.nodeId)
          }}
          onCancel={() => setConfirmRollback(false)}
        />
      </TableCell>
    </TableRow>
  )
}

/** 节点区属性。 */
export interface SystemUpdateNodesSectionProps {
  /** 各节点版本对比结果（空数组渲染「暂无节点」空行）。 */
  nodes: SystemUpdateComponentStatus[]
  /** 更新源最新版本。 */
  latest: string
  /** Worker 缓存行的版本口径（CP 当前版本，缺省回落 latest）。 */
  workerAssetVersion: string
  /** 全网升级在途：禁用「全网升级」按钮与各节点单点动作。 */
  rolloutRunning: boolean
  /** 单节点升级/回滚的在途目标；null=空闲。 */
  nodePending: SystemUpdateNodePending | null
  /** 单节点升级上报。 */
  onUpgradeNode: (nodeId: number) => void
  /** 单节点回滚上报。 */
  onRollbackNode: (nodeId: number) => void
  /** 打开全网升级配置弹窗（开合与二次确认留在本视图）。 */
  onUpgradeAll: () => void
  /** DangerConfirm 的平台范围门禁结果。 */
  dangerAllowed: boolean
  /** Worker 二进制缓存条目。 */
  workerAssets?: SystemUpdateWorkerAssetEntry[]
  /** Worker 缓存取数中。 */
  workerAssetsFetching: boolean
  /** Worker 缓存取数失败的后端消息。 */
  workerAssetsErrorMessage?: string
  /** 预缓存在途的目标平台。 */
  pendingWorkerAsset: SystemUpdateWorkerAssetTarget | null
  /** 预缓存上报。 */
  onCacheWorkerAsset: (target: SystemUpdateWorkerAssetTarget) => void
  /** 提示通道（复制回执）。 */
  onNotify?: (kind: 'success' | 'error', message: string) => void
}

/** 节点区：全网升级按钮 + Worker 缓存面板 + 各节点版本对比与单节点升级。 */
export function SystemUpdateNodesSection({
  nodes,
  latest,
  workerAssetVersion,
  rolloutRunning,
  nodePending,
  onUpgradeNode,
  onRollbackNode,
  onUpgradeAll,
  dangerAllowed,
  workerAssets,
  workerAssetsFetching,
  workerAssetsErrorMessage,
  pendingWorkerAsset,
  onCacheWorkerAsset,
  onNotify,
}: SystemUpdateNodesSectionProps) {
  const { t } = useTranslation()
  const anyUpgradable = nodes.some((n) => n.updateAvailable)

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <h2 className="text-lg font-semibold">{t('systemUpdate.nodes', '节点（Worker）')}</h2>
        <Button size="sm" variant="outline" disabled={!anyUpgradable || rolloutRunning} onClick={onUpgradeAll}>
          <ArrowUpCircle className="size-4" />
          {rolloutRunning ? t('systemUpdate.rolloutInProgress', '升级进行中…') : t('systemUpdate.upgradeAll', '全网升级')}
        </Button>
      </div>

      <SystemUpdateWorkerAssetsPanel
        nodes={nodes}
        version={workerAssetVersion}
        assets={workerAssets}
        assetsFetching={workerAssetsFetching}
        assetsErrorMessage={workerAssetsErrorMessage}
        pendingAsset={pendingWorkerAsset}
        onCacheAsset={onCacheWorkerAsset}
        onNotify={onNotify}
      />

      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('common.name', '名称')}</TableHead>
              <TableHead>{t('common.status', '状态')}</TableHead>
              <TableHead>{t('systemUpdate.platform', '平台')}</TableHead>
              <TableHead>{t('systemUpdate.current', '当前版本')}</TableHead>
              <TableHead>{t('systemUpdate.updateState', '更新状态')}</TableHead>
              <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {nodes.map((n) => (
              <SystemUpdateNodeRow
                key={n.nodeId}
                node={n}
                latest={latest}
                rolloutRunning={rolloutRunning}
                // 先判 nodePending 非空再比 nodeId：nodeId 本身可选，`undefined === undefined` 会误命中。
                pending={nodePending && nodePending.nodeId === n.nodeId ? nodePending.action : null}
                dangerAllowed={dangerAllowed}
                onUpgrade={onUpgradeNode}
                onRollback={onRollbackNode}
              />
            ))}
            {nodes.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="h-16 text-center text-muted-foreground">
                  {t('systemUpdate.noNodes', '暂无节点')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

/** Control Plane 自更新卡片属性。 */
export interface SystemUpdateControlPlaneCardProps {
  /** CP 版本对比结果。 */
  cp: SystemUpdateComponentStatus
  /** 更新源最新版本（升级确认需逐字输入的文本）。 */
  latest: string
  /** 自升级在途。 */
  upgrading: boolean
  /** 回滚在途。 */
  rollingBack: boolean
  /** DangerConfirm 的平台范围门禁结果。 */
  dangerAllowed: boolean
  /** 自升级上报（容器执行 mutation + toast + 延迟刷新检查结果）。 */
  onUpgrade: () => void
  /** 回滚上报。 */
  onRollback: () => void
}

/** Control Plane 自更新卡片：当前版本 vs 最新 + 自更新按钮 + 回滚（有备份时）。 */
export function SystemUpdateControlPlaneCard({
  cp,
  latest,
  upgrading,
  rollingBack,
  dangerAllowed,
  onUpgrade,
  onRollback,
}: SystemUpdateControlPlaneCardProps) {
  const { t } = useTranslation()
  const [confirm, setConfirm] = useState(false)
  const [confirmRollback, setConfirmRollback] = useState(false)
  const hasBackup = !!cp.backupVersion

  return (
    <div className="rounded-lg border p-4">
      <div className="flex items-center justify-between flex-wrap gap-3">
        <div className="flex items-center gap-3">
          <ServerCog className="size-5 text-primary" />
          <div>
            <div className="font-medium">{t('systemUpdate.controlPlane', 'Control Plane（控制台）')}</div>
            <div className="text-xs text-muted-foreground font-mono mt-0.5">
              {cp.os}/{cp.arch} · {t('systemUpdate.current', '当前')} {cp.currentVersion || '-'}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <SystemUpdateVersionBadge current={cp.currentVersion} latest={latest} updateAvailable={cp.updateAvailable} artifactAvailable={cp.artifactAvailable} />
          <Button
            size="sm"
            variant="outline"
            disabled={!hasBackup || rollingBack}
            onClick={() => setConfirmRollback(true)}
            title={hasBackup ? undefined : t('systemUpdate.noBackup', '无可回滚的备份')}
          >
            <ArrowDownCircle className="size-4" />
            {hasBackup
              ? t('systemUpdate.rollbackTo', '回滚 v{{v}}', { v: cp.backupVersion })
              : t('systemUpdate.rollback', '回滚')}
          </Button>
          <Button
            size="sm"
            disabled={!cp.updateAvailable || upgrading}
            onClick={() => setConfirm(true)}
          >
            <ArrowUpCircle className="size-4" />
            {t('systemUpdate.upgrade', '升级')}
          </Button>
        </div>
      </div>

      {/* 危险动作边界（FR-059，逐字保持）：scope=platform + confirmText=目标版本号（须逐字输入）。
          门禁结果 allowed 由容器注入，本视图不得自行判定或放宽。 */}
      <DangerConfirm
        open={confirm}
        title={t('systemUpdate.cpUpgradeConfirm', '确定升级 Control Plane？')}
        description={t('systemUpdate.cpUpgradeConfirmDesc', '将下载新版二进制、sha256 校验后替换并平滑重启控制台。重启期间 Web 短暂不可用，重连后即为新版本。')}
        scope="platform"
        allowed={dangerAllowed}
        confirmLabel={t('systemUpdate.upgrade', '升级')}
        confirmText={latest}
        onConfirm={() => {
          setConfirm(false)
          onUpgrade()
        }}
        onCancel={() => setConfirm(false)}
      />

      <DangerConfirm
        open={confirmRollback}
        title={t('systemUpdate.cpRollbackConfirm', '确定回滚 Control Plane？')}
        description={t('systemUpdate.cpRollbackConfirmDesc', '将把控制台换回升级前备份（v{{v}}）、sha256 校验后替换并平滑重启。重启期间 Web 短暂不可用，重连后即为旧版本。', { v: cp.backupVersion })}
        scope="platform"
        allowed={dangerAllowed}
        confirmLabel={t('systemUpdate.rollback', '回滚')}
        confirmText={cp.backupVersion}
        onConfirm={() => {
          setConfirmRollback(false)
          onRollback()
        }}
        onCancel={() => setConfirmRollback(false)}
      />
    </div>
  )
}

/** rollout 编排阶段徽章属性。 */
export interface SystemUpdateRolloutPhaseBadgeProps {
  /** 编排阶段：canary | rolling | aborted | completed；其余值不渲染。 */
  phase: string
}

/** rollout 编排阶段徽章（FR-155）：金丝雀 / 滚动 / 已中止 / 已完成。 */
export function SystemUpdateRolloutPhaseBadge({ phase }: SystemUpdateRolloutPhaseBadgeProps) {
  const { t } = useTranslation()
  switch (phase) {
    case 'canary':
      return <Badge variant="outline" className="border-sky-500/50 text-sky-600">{t('systemUpdate.phaseCanary', '金丝雀')}</Badge>
    case 'rolling':
      return <Badge variant="outline" className="border-amber-500/50 text-amber-600">{t('systemUpdate.phaseRolling', '滚动')}</Badge>
    case 'aborted':
      return <Badge variant="outline" className="text-destructive border-destructive/40">{t('systemUpdate.phaseAborted', '已中止')}</Badge>
    case 'completed':
      return <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">{t('systemUpdate.phaseCompleted', '已完成')}</Badge>
    default:
      return null
  }
}

/** rollout 整体状态徽章属性。 */
export interface SystemUpdateRolloutStateBadgeProps {
  /** 整体状态：running | completed | 其余视为空闲。 */
  state: string
}

/** rollout 整体状态徽章。 */
export function SystemUpdateRolloutStateBadge({ state }: SystemUpdateRolloutStateBadgeProps) {
  const { t } = useTranslation()
  if (state === 'running') {
    return <Badge variant="outline" className="border-amber-500/50 text-amber-600"><RefreshCw className="size-3.5 animate-spin" /> {t('systemUpdate.stateRunning', '进行中')}</Badge>
  }
  if (state === 'completed') {
    return <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">{t('systemUpdate.stateCompleted', '已完成')}</Badge>
  }
  return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.stateIdle', '空闲')}</Badge>
}

/** rollout 单节点状态徽章属性。 */
export interface SystemUpdateRolloutNodeBadgeProps {
  /** 节点状态：succeeded | failed | upgrading | skipped | 其余视为待处理。 */
  state: SystemUpdateRolloutNode['state']
}

/** rollout 单节点状态徽章。 */
export function SystemUpdateRolloutNodeBadge({ state }: SystemUpdateRolloutNodeBadgeProps) {
  const { t } = useTranslation()
  switch (state) {
    case 'succeeded':
      return <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">{t('systemUpdate.nodeSucceeded', '成功')}</Badge>
    case 'failed':
      return <Badge variant="outline" className="text-destructive border-destructive/40">{t('systemUpdate.nodeFailed', '失败')}</Badge>
    case 'upgrading':
      return <Badge variant="outline" className="border-amber-500/50 text-amber-600"><RefreshCw className="size-3.5 animate-spin" /> {t('systemUpdate.nodeUpgrading', '升级中')}</Badge>
    case 'skipped':
      return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.nodeSkipped', '已跳过')}</Badge>
    default:
      return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.nodePending', '待处理')}</Badge>
  }
}

/** 全网升级进度面板属性。 */
export interface SystemUpdateRolloutPanelProps {
  /** rollout 进度快照（容器短轮询后注入）。 */
  rollout: SystemUpdateRollout
}

/** 全网升级进度面板：聚合计数 + 逐节点状态。 */
export function SystemUpdateRolloutPanel({ rollout }: SystemUpdateRolloutPanelProps) {
  const { t } = useTranslation()
  return (
    <div className="rounded-lg border p-4 space-y-3">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <h2 className="text-lg font-semibold">{t('systemUpdate.rolloutTitle', '全网升级进度')}</h2>
        <div className="flex items-center gap-2">
          {rollout.phase && <SystemUpdateRolloutPhaseBadge phase={rollout.phase} />}
          <SystemUpdateRolloutStateBadge state={rollout.state} />
        </div>
      </div>
      <div className="text-sm text-muted-foreground flex flex-wrap gap-x-4 gap-y-1">
        <span>{t('systemUpdate.rolloutTarget', '目标版本')}：<span className="font-mono text-foreground">{rollout.targetVersion || t('systemUpdate.feedLatest', '源最新')}</span></span>
        <span>{t('systemUpdate.rolloutTotal', '共 {{n}} 个', { n: rollout.total })}</span>
        <span className="text-emerald-600">{t('systemUpdate.rolloutSucceeded', '成功 {{n}}', { n: rollout.succeeded })}</span>
        <span className="text-destructive">{t('systemUpdate.rolloutFailedCount', '失败 {{n}}', { n: rollout.failed })}</span>
        <span>{t('systemUpdate.rolloutPending', '待处理 {{n}}', { n: rollout.pending })}</span>
        {/* 金丝雀分批回显（FR-155）：仅在设了金丝雀或分批时展示，避免污染原「串行全部」的简洁面板。 */}
        {!!rollout.canarySize && (
          <span>{t('systemUpdate.rolloutCanary', '金丝雀 {{n}}', { n: rollout.canarySize })}</span>
        )}
        {!!rollout.currentBatch && (rollout.canarySize || (rollout.batchSize ?? 0) > 0) && (
          <span>{t('systemUpdate.rolloutCurrentBatch', '第 {{n}} 批', { n: rollout.currentBatch })}</span>
        )}
      </div>

      <div className="overflow-hidden rounded-md border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('common.name', '名称')}</TableHead>
              <TableHead>{t('common.status', '状态')}</TableHead>
              <TableHead>{t('systemUpdate.versionChange', '版本变化')}</TableHead>
              <TableHead>{t('systemUpdate.detail', '详情')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rollout.nodes.map((n) => (
              <TableRow key={n.nodeId}>
                <TableCell className="font-medium">{n.name}</TableCell>
                <TableCell><SystemUpdateRolloutNodeBadge state={n.state} /></TableCell>
                <TableCell className="font-mono text-xs">
                  {n.fromVersion || n.toVersion ? `${n.fromVersion || '?'} → ${n.toVersion || '?'}` : '-'}
                </TableCell>
                <TableCell className="text-xs text-destructive">{n.error || ''}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

/**
 * 受控边界（ADR-097 a 范式）：**不取数、不发请求、不弹 toast**。
 * - 三份查询的结果与错误态经 props 注入（容器调 `useSelfUpdateCheck` / `useRollout` / `useWorkerAssets`）；
 *   错误文案由容器从 axios 错误里取后端 message 后注入，故视图只认「有没有错误」。
 * - 八个写动作（检查更新、CP 升级/回滚、节点升级/回滚、全网升级、预缓存、复制回执）一律以回调上报，
 *   由容器执行 mutation 与 toast 文案；复制回执走 `onNotify` 通道（包内不引 sonner）。
 * - **危险动作边界逐字保持**：四处 `DangerConfirm` 的 scope=platform、confirmText（CP 升级=目标版本号、
 *   CP 回滚=备份版本号、节点升级/回滚=无）、按钮禁用判据、确认框开合时机（先关窗再发请求）与迁包前完全一致；
 *   门禁结果经 `dangerAllowed` 注入（容器读登录态角色），视图既不判定也不放宽。
 * - **受控状态归属**：本页查询键固定，没有「一变就重新取数」的状态；因此弹窗开合（全网升级配置/二次确认、
 *   CP 与节点行的二次确认）与金丝雀草稿（金丝雀数、每批数、失败即中止）都留包内——它们是纯 UI 状态。
 *   只有「在途目标」这类由 mutation 派生的展示态由容器注入（`nodePending` / `pendingWorkerAsset` /
 *   `cpUpgrading` / `cpRollingBack`），因为视图不持 mutation。
 * - 角色兜底（`isPlatformAdmin`）与 `role` 取值留在容器；视图只按注入结果渲染 forbidden 提示。
 */
export interface SystemUpdatePageViewProps {
  /** 是否平台管理员：false 时整页渲染 forbidden 提示（路由守卫之外的纵深防御）。 */
  isPlatformAdmin: boolean
  /** 四处危险确认的平台范围门禁结果（应用侧读登录态角色后注入）。 */
  dangerAllowed: boolean
  /** `GET /self-update/check` 的结果；缺省表示尚未拿到任何结果（渲染引导占位）。 */
  check?: SystemUpdateCheckResult
  /** 检查失败的后端消息（缺省=无错误）；仅当尚无 check 结果时渲染整屏错误横幅。 */
  checkErrorMessage?: string
  /** 检查更新在途（手动 live 刷新或缓存读取中）：页头指示与按钮禁用。 */
  refreshing: boolean
  /** 「检查更新」上报（容器执行 live 刷新，失败 toast 但保留旧缓存）。 */
  onRefresh: () => void
  /** CP 自升级在途。 */
  cpUpgrading: boolean
  /** CP 回滚在途。 */
  cpRollingBack: boolean
  /** CP 自升级上报。 */
  onUpgradeControlPlane: () => void
  /** CP 回滚上报。 */
  onRollbackControlPlane: () => void
  /** 单节点升级/回滚的在途目标；null=空闲。 */
  nodePending: SystemUpdateNodePending | null
  /** 单节点升级上报。 */
  onUpgradeNode: (nodeId: number) => void
  /** 单节点回滚上报。 */
  onRollbackNode: (nodeId: number) => void
  /**
   * 全网升级上报：草稿原样传出（空串=不设该项），折算请求体在容器
   * （`Math.max(0, Number(x))` 与「未设金丝雀则不传 abortOnCanaryFailure」）。
   */
  onUpgradeAll: (draft: SystemUpdateRolloutDraft) => void
  /** 全网升级进度快照（容器短轮询后注入）；`state === 'idle'` 时不渲染进度面板。 */
  rollout?: SystemUpdateRollout
  /** Worker 二进制缓存条目。 */
  workerAssets?: SystemUpdateWorkerAssetEntry[]
  /** Worker 缓存取数中。 */
  workerAssetsFetching: boolean
  /** Worker 缓存取数失败的后端消息（缺省=无错误）。 */
  workerAssetsErrorMessage?: string
  /** 预缓存在途的目标平台；null=空闲。 */
  pendingWorkerAsset: SystemUpdateWorkerAssetTarget | null
  /** 预缓存上报。 */
  onCacheWorkerAsset: (target: SystemUpdateWorkerAssetTarget) => void
  /** 提示通道（复制 SHA256 的回执）；包内不引 sonner。 */
  onNotify?: (kind: 'success' | 'error', message: string) => void
}

/**
 * 面板自更新页（FR-081，见 ADR-020）。
 * 平台管理员可检查更新（CP 自身 + 各节点版本对比）、CP 自更新、单节点升级、全网逐节点编排。
 * 入口在侧栏「设置」组，仅平台管理员可见；容器侧角色兜底，后端 RBAC 同样强制。
 * 升级为危险操作（二进制热替换 + 平滑重启），统一走 DangerConfirm + scope=platform 二次确认（FR-059）。
 * i18n（FR-016）+ 暗/亮色（FR-026，全程用主题 token）。
 */
export function SystemUpdatePageView({
  isPlatformAdmin,
  dangerAllowed,
  check,
  checkErrorMessage,
  refreshing,
  onRefresh,
  cpUpgrading,
  cpRollingBack,
  onUpgradeControlPlane,
  onRollbackControlPlane,
  nodePending,
  onUpgradeNode,
  onRollbackNode,
  onUpgradeAll,
  rollout,
  workerAssets,
  workerAssetsFetching,
  workerAssetsErrorMessage,
  pendingWorkerAsset,
  onCacheWorkerAsset,
  onNotify,
}: SystemUpdatePageViewProps) {
  const { t } = useTranslation()
  // 全网升级两步走：先开配置弹窗（金丝雀数/每批数/失败即中止，FR-155），确认后再走 DangerConfirm 二次确认。
  const [configAll, setConfigAll] = useState(false)
  const [confirmAll, setConfirmAll] = useState(false)
  const [canarySize, setCanarySize] = useState('')
  const [batchSize, setBatchSize] = useState('')
  const [abortOnCanaryFailure, setAbortOnCanaryFailure] = useState(true)

  if (!isPlatformAdmin) {
    return (
      <div className="grid h-full place-items-center text-sm text-muted-foreground">
        {t('systemUpdate.forbidden')}
      </div>
    )
  }

  const result = check
  const notConfigured = result ? !result.configured : false

  // 「上次检查」相对时间取缓存结果的 checkedAt；刷新中（手动或后台）统一指示。
  const lastChecked = result?.checkedAt ? formatRelativeTime(result.checkedAt) : ''
  const rolloutRunning = rollout?.state === 'running'

  return (
    // 全量对齐：外壳与页头改用布局层原语。原先无 data-page，迁移时补上。
    <PageShell data-page="system-update">
      <PageHeader
        title={t('systemUpdate.title', '系统更新')}
        description={t('systemUpdate.subtitle', '检查并升级 Control Plane 与各节点 Worker 的二进制版本。升级经 sha256 校验后热替换并平滑重启，daemon 模式下不影响运行中的游戏服。')}
        actions={
          <>
            {(lastChecked || refreshing) && (
              <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                <Clock className={refreshing ? 'size-3.5 animate-spin' : 'size-3.5'} />
                {refreshing
                  ? t('systemUpdate.checking', '正在检查…')
                  : t('systemUpdate.lastChecked', '上次检查：{{time}}', { time: lastChecked })}
              </span>
            )}
            <Button onClick={onRefresh} disabled={refreshing}>
              <RefreshCw className={refreshing ? 'size-4 animate-spin' : 'size-4'} />
              {t('systemUpdate.checkUpdate', '检查更新')}
            </Button>
          </>
        }
      />

      {/* 刷新失败保留旧缓存数据，仅在「从未有过任何结果」时才整屏报错；否则错误经 toast 提示（容器侧）。 */}
      {checkErrorMessage && !result && (
        <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          <span>{checkErrorMessage}</span>
        </div>
      )}

      {/* 缓存为空且尚未拉到结果时的占位（首次进页且刷新未回时短暂可见）。 */}
      {!result && !checkErrorMessage && (
        <div className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          {refreshing
            ? t('systemUpdate.checking', '正在检查…')
            : t('systemUpdate.notCheckedYet', '点击「检查更新」拉取更新源并对比各组件版本。')}
        </div>
      )}

      {result && notConfigured && (
        <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm">
          <AlertCircle className="mt-0.5 size-4 shrink-0 text-amber-600" />
          <span>{t('systemUpdate.notConfigured', '未配置更新源（feed_url）。请在 control-plane.yml 的 update 段配置更新源后再检查更新。')}</span>
        </div>
      )}

      {/* 未配源也渲染当前版本（后端 CheckUpdate 无条件返回 CP+各节点版本，FR-110）；
          仅「最新版本对比」与升级动作依赖配源——配源时显示，未配源时按钮自然禁用。 */}
      {result && (
        <>
          {result.configured && (
            <div className="space-y-2">
              <div className="text-sm text-muted-foreground">
                {t('systemUpdate.latestVersion', '更新源最新版本')}：
                <span className="font-mono font-medium text-foreground">{result.latestVersion || '-'}</span>
                {result.source && (
                  <span className="ml-2 text-xs font-mono text-muted-foreground">({result.source})</span>
                )}
              </div>
              {result.notes && (
                <div className="rounded-md border bg-muted/40 px-3 py-2">
                  <div className="text-xs font-medium text-muted-foreground mb-1">
                    {t('systemUpdate.releaseNotes', '更新说明')}
                  </div>
                  <ReleaseNotes markdown={result.notes} />
                </div>
              )}
            </div>
          )}

          <SystemUpdateControlPlaneCard
            cp={result.controlPlane}
            latest={result.latestVersion}
            upgrading={cpUpgrading}
            rollingBack={cpRollingBack}
            dangerAllowed={dangerAllowed}
            onUpgrade={onUpgradeControlPlane}
            onRollback={onRollbackControlPlane}
          />

          <SystemUpdateNodesSection
            nodes={result.nodes ?? []}
            latest={result.latestVersion}
            workerAssetVersion={result.controlPlane.currentVersion || result.latestVersion}
            rolloutRunning={rolloutRunning}
            nodePending={nodePending}
            onUpgradeNode={onUpgradeNode}
            onRollbackNode={onRollbackNode}
            onUpgradeAll={() => setConfigAll(true)}
            dangerAllowed={dangerAllowed}
            workerAssets={workerAssets}
            workerAssetsFetching={workerAssetsFetching}
            workerAssetsErrorMessage={workerAssetsErrorMessage}
            pendingWorkerAsset={pendingWorkerAsset}
            onCacheWorkerAsset={onCacheWorkerAsset}
            onNotify={onNotify}
          />
        </>
      )}

      {rollout && rollout.state !== 'idle' && <SystemUpdateRolloutPanel rollout={rollout} />}

      {/* 全网升级第一步：金丝雀分批配置（FR-155）。留空即无金丝雀/剩余一批，等价原串行全部。 */}
      <Dialog open={configAll} onOpenChange={(v) => { if (!v) setConfigAll(false) }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t('systemUpdate.canaryConfigTitle', '全网升级策略')}</DialogTitle>
            <DialogDescription>
              {t('systemUpdate.canaryConfigDesc', '可先升级少量金丝雀节点验证无误后再分批推进全网；留空则一次串行升级所有节点。')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-1">
            <div className="space-y-1.5">
              <Label htmlFor="rollout-canary-size">{t('systemUpdate.canarySize', '金丝雀节点数')}</Label>
              <Input
                id="rollout-canary-size"
                type="number"
                min={0}
                inputMode="numeric"
                value={canarySize}
                onChange={(e) => setCanarySize(e.target.value)}
                placeholder={t('systemUpdate.canaryNonePlaceholder', '0 = 不设金丝雀')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="rollout-batch-size">{t('systemUpdate.batchSize', '每批节点数')}</Label>
              <Input
                id="rollout-batch-size"
                type="number"
                min={0}
                inputMode="numeric"
                value={batchSize}
                onChange={(e) => setBatchSize(e.target.value)}
                placeholder={t('systemUpdate.batchAllPlaceholder', '留空 = 剩余一次全部')}
              />
            </div>
            <label className="flex items-center gap-2 text-sm" htmlFor="rollout-abort-canary">
              <Checkbox
                id="rollout-abort-canary"
                checked={abortOnCanaryFailure}
                onCheckedChange={(v) => setAbortOnCanaryFailure(v === true)}
              />
              {t('systemUpdate.abortOnCanaryFailure', '金丝雀失败即中止（不再升级剩余节点）')}
            </label>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfigAll(false)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button onClick={() => { setConfigAll(false); setConfirmAll(true) }}>
              {t('common.continue', '继续')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 危险动作边界（FR-059，逐字保持）：scope=platform、无 confirmText（全网升级以「全部在线节点」为对象，
          没有可逐字输入的单一名），开合时机与迁包前一致（确认即关窗，再交由容器发起）。 */}
      <DangerConfirm
        open={confirmAll}
        title={t('systemUpdate.upgradeAllConfirm', '确定升级全网节点？')}
        description={t('systemUpdate.upgradeAllConfirmDesc', '将对所有在线节点逐个下发升级（串行）。每个节点下载校验后热替换并重启 Worker；daemon 模式下游戏服不掉。')}
        scope="platform"
        allowed={dangerAllowed}
        confirmLabel={t('systemUpdate.upgradeAll', '全网升级')}
        onConfirm={() => {
          setConfirmAll(false)
          onUpgradeAll({ canarySize, batchSize, abortOnCanaryFailure })
        }}
        onCancel={() => setConfirmAll(false)}
      />
    </PageShell>
  )
}

export default SystemUpdatePageView
