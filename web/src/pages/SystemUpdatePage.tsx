import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { RefreshCw, ArrowUpCircle, ArrowDownCircle, ServerCog, AlertCircle, CheckCircle2, Clock, Download } from 'lucide-react'
import {
  useSelfUpdateCheck,
  useRefreshSelfUpdateCheck,
  useRollout,
  useWorkerAssets,
  useCacheWorkerAsset,
  useUpgradeControlPlane,
  useUpgradeNode,
  useUpgradeAll,
  useRollbackControlPlane,
  useRollbackNode,
  type ComponentStatus,
  type WorkerAssetCacheEntry,
  type Rollout,
  type RolloutNodeState,
} from '@/api/selfUpdate'
import { useAuthStore } from '@/stores/auth'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/DangerConfirm'
import { ReleaseNotes } from '@/components/ReleaseNotes'
import { formatCacheBytes } from '@/lib/artifact-cache'
import { formatRelativeTime } from '@/lib/relative-time'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

type WorkerAssetRow = WorkerAssetCacheEntry & { key: string }

const workerAssetKey = (version: string, os: string, arch: string) => `${version}/${os}/${arch}`

function buildWorkerAssetRows(
  nodes: ComponentStatus[],
  assets: WorkerAssetCacheEntry[],
  version: string,
): WorkerAssetRow[] {
  const rows = new Map<string, WorkerAssetRow>()
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

function formatWorkerAssetTime(value?: string) {
  return value ? value.replace('T', ' ').replace(/(\.\d+)?Z$/, ' UTC') : '-'
}

/**
 * 面板自更新页（FR-081，见 ADR-020）。
 * 平台管理员可检查更新（CP 自身 + 各节点版本对比）、CP 自更新、单节点升级、全网逐节点编排。
 * 入口在侧栏「设置」组，仅平台管理员可见；本页再以角色兜底，后端 RBAC 同样强制。
 * 升级为危险操作（二进制热替换 + 平滑重启），统一走 DangerConfirm + scope=platform 二次确认（FR-059）。
 * i18n（FR-016）+ 暗/亮色（FR-026，全程用主题 token）。
 */
export default function SystemUpdatePage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isPlatformAdmin = role === ROLE_PLATFORM_ADMIN

  // check 读服务端缓存（进页即时回显，FR-186）；refresh 走 live 检查并覆盖缓存。
  const check = useSelfUpdateCheck()
  const refresh = useRefreshSelfUpdateCheck()
  const upgradeAll = useUpgradeAll()

  // FIX-6：进页只读缓存（useSelfUpdateCheck = GET /check，无副作用）、绝不自动 live 刷新。
  // 原「进页静默刷新一次」每次点开都触发 live 检查 + UPDATE self_update_check_caches（慢 + 写库 + 联网），
  // 改为仅「检查更新」按钮显式 live 刷新；缓存为空时页面提示点击检查更新。

  // rollout 在运行中时短轮询，空闲/完成后停（轮询逻辑在 hook 内）。
  const rolloutQ = useRollout()
  const rolloutRunning = rolloutQ.data?.state === 'running'

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

  const result = check.data
  const notConfigured = result ? !result.configured : false

  // 手动「检查更新」= 显式 live 刷新（失败 toast 但保留旧缓存数据，FR-186）。
  const doRefresh = () => {
    refresh.mutate(undefined, {
      onError: (e) => toast.error(errMsg(e, t('systemUpdate.checkFailed', '检查更新失败'))),
    })
  }

  // 刷新中（手动或后台）统一指示；「上次检查」相对时间取缓存结果的 checkedAt。
  const refreshing = refresh.isPending || check.isFetching
  const lastChecked = result?.checkedAt ? formatRelativeTime(result.checkedAt) : ''

  const doUpgradeAll = () => {
    setConfirmAll(false)
    // 空输入=不设该项：canarySize 省略=无金丝雀、batchSize 省略=剩余全部一批（等价原行为）。
    const canary = canarySize.trim() === '' ? undefined : Math.max(0, Number(canarySize))
    const batch = batchSize.trim() === '' ? undefined : Math.max(0, Number(batchSize))
    upgradeAll.mutate(
      {
        canarySize: canary,
        batchSize: batch,
        abortOnCanaryFailure: canary ? abortOnCanaryFailure : undefined,
      },
      {
        onSuccess: () => {
          toast.success(t('systemUpdate.rolloutStarted', '全网升级已发起'))
          void rolloutQ.refetch()
        },
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.rolloutStartFailed', '发起全网升级失败'))),
      },
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between flex-wrap gap-3">
        <div>
          <h1 className="text-2xl font-bold">{t('systemUpdate.title', '系统更新')}</h1>
          <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
            {t('systemUpdate.subtitle', '检查并升级 Control Plane 与各节点 Worker 的二进制版本。升级经 sha256 校验后热替换并平滑重启，daemon 模式下不影响运行中的游戏服。')}
          </p>
        </div>
        <div className="flex items-center gap-3">
          {(lastChecked || refreshing) && (
            <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
              <Clock className={refreshing ? 'size-3.5 animate-spin' : 'size-3.5'} />
              {refreshing
                ? t('systemUpdate.checking', '正在检查…')
                : t('systemUpdate.lastChecked', '上次检查：{{time}}', { time: lastChecked })}
            </span>
          )}
          <Button onClick={doRefresh} disabled={refreshing}>
            <RefreshCw className={refreshing ? 'size-4 animate-spin' : 'size-4'} />
            {t('systemUpdate.checkUpdate', '检查更新')}
          </Button>
        </div>
      </div>

      {/* 刷新失败保留旧缓存数据，仅在「从未有过任何结果」时才整屏报错；否则错误经 toast 提示（doRefresh）。 */}
      {check.isError && !result && (
        <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          <span>{errMsg(check.error, t('systemUpdate.checkFailed', '检查更新失败'))}</span>
        </div>
      )}

      {/* 缓存为空且尚未拉到结果时的占位（首次进页且后台刷新未回时短暂可见）。 */}
      {!result && !check.isError && (
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

          <ControlPlaneCard cp={result.controlPlane} latest={result.latestVersion} onUpgraded={() => refresh.mutate(undefined)} />

          <NodesSection
            nodes={result.nodes ?? []}
            latest={result.latestVersion}
            workerAssetVersion={result.controlPlane.currentVersion || result.latestVersion}
            rolloutRunning={rolloutRunning}
            onUpgradeAll={() => setConfigAll(true)}
            onUpgraded={() => refresh.mutate(undefined)}
          />
        </>
      )}

      {rolloutQ.data && rolloutQ.data.state !== 'idle' && <RolloutPanel rollout={rolloutQ.data} />}

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

      <DangerConfirm
        open={confirmAll}
        title={t('systemUpdate.upgradeAllConfirm', '确定升级全网节点？')}
        description={t('systemUpdate.upgradeAllConfirmDesc', '将对所有在线节点逐个下发升级（串行）。每个节点下载校验后热替换并重启 Worker；daemon 模式下游戏服不掉。')}
        scope="platform"
        confirmLabel={t('systemUpdate.upgradeAll', '全网升级')}
        onConfirm={doUpgradeAll}
        onCancel={() => setConfirmAll(false)}
      />
    </div>
  )
}

/** Control Plane 自更新卡片：当前版本 vs 最新 + 自更新按钮 + 回滚（有备份时）。 */
function ControlPlaneCard({ cp, latest, onUpgraded }: { cp: ComponentStatus; latest: string; onUpgraded: () => void }) {
  const { t } = useTranslation()
  const upgrade = useUpgradeControlPlane()
  const rollback = useRollbackControlPlane()
  const [confirm, setConfirm] = useState(false)
  const [confirmRollback, setConfirmRollback] = useState(false)
  const hasBackup = !!cp.backupVersion

  const doUpgrade = () => {
    setConfirm(false)
    upgrade.mutate(undefined, {
      onSuccess: (ack) => {
        toast.success(
          t('systemUpdate.cpUpgradeStarted', '控制台升级已开始（{{from}} → {{to}}），即将平滑重启', {
            from: ack.fromVersion,
            to: ack.toVersion,
          }),
        )
        // CP 升级后会重启，稍后刷新检查结果（重连后版本应为新版）。
        setTimeout(onUpgraded, 4000)
      },
      onError: (e) => toast.error(errMsg(e, t('systemUpdate.cpUpgradeFailed', '控制台升级失败'))),
    })
  }

  const doRollback = () => {
    setConfirmRollback(false)
    rollback.mutate(undefined, {
      onSuccess: (ack) => {
        toast.success(
          t('systemUpdate.cpRollbackStarted', '控制台已回滚（{{from}} → {{to}}），即将平滑重启', {
            from: ack.fromVersion,
            to: ack.toVersion,
          }),
        )
        setTimeout(onUpgraded, 4000)
      },
      onError: (e) => toast.error(errMsg(e, t('systemUpdate.cpRollbackFailed', '控制台回滚失败'))),
    })
  }

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
          <VersionBadge current={cp.currentVersion} latest={latest} updateAvailable={cp.updateAvailable} artifactAvailable={cp.artifactAvailable} />
          <Button
            size="sm"
            variant="outline"
            disabled={!hasBackup || rollback.isPending}
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
            disabled={!cp.updateAvailable || upgrade.isPending}
            onClick={() => setConfirm(true)}
          >
            <ArrowUpCircle className="size-4" />
            {t('systemUpdate.upgrade', '升级')}
          </Button>
        </div>
      </div>

      <DangerConfirm
        open={confirm}
        title={t('systemUpdate.cpUpgradeConfirm', '确定升级 Control Plane？')}
        description={t('systemUpdate.cpUpgradeConfirmDesc', '将下载新版二进制、sha256 校验后替换并平滑重启控制台。重启期间 Web 短暂不可用，重连后即为新版本。')}
        scope="platform"
        confirmLabel={t('systemUpdate.upgrade', '升级')}
        confirmText={latest}
        onConfirm={doUpgrade}
        onCancel={() => setConfirm(false)}
      />

      <DangerConfirm
        open={confirmRollback}
        title={t('systemUpdate.cpRollbackConfirm', '确定回滚 Control Plane？')}
        description={t('systemUpdate.cpRollbackConfirmDesc', '将把控制台换回升级前备份（v{{v}}）、sha256 校验后替换并平滑重启。重启期间 Web 短暂不可用，重连后即为旧版本。', { v: cp.backupVersion })}
        scope="platform"
        confirmLabel={t('systemUpdate.rollback', '回滚')}
        confirmText={cp.backupVersion}
        onConfirm={doRollback}
        onCancel={() => setConfirmRollback(false)}
      />
    </div>
  )
}

/** 节点区：全网升级按钮 + 各节点版本对比与单节点升级。 */
function NodesSection({
  nodes,
  latest,
  workerAssetVersion,
  rolloutRunning,
  onUpgradeAll,
  onUpgraded,
}: {
  nodes: ComponentStatus[]
  latest: string
  workerAssetVersion: string
  rolloutRunning: boolean
  onUpgradeAll: () => void
  onUpgraded: () => void
}) {
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

      <WorkerAssetsPanel nodes={nodes} version={workerAssetVersion} />

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
              <NodeRow key={n.nodeId} node={n} latest={latest} disabled={rolloutRunning} onUpgraded={onUpgraded} />
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

/** Worker 二进制 CP 代理缓存状态：按节点平台展示，并允许手动预缓存。 */
function WorkerAssetsPanel({ nodes, version }: { nodes: ComponentStatus[]; version: string }) {
  const { t } = useTranslation()
  const assets = useWorkerAssets()
  const cacheAsset = useCacheWorkerAsset()
  const rows = buildWorkerAssetRows(nodes, assets.data ?? [], version)
  const busyKey = cacheAsset.isPending
    ? workerAssetKey(version, cacheAsset.variables?.os ?? '', cacheAsset.variables?.arch ?? '')
    : ''
  const title = t('systemUpdate.workerAssetsTitle', 'Worker 二进制缓存')

  const doCache = (row: WorkerAssetRow) => {
    cacheAsset.mutate(
      { os: row.os, arch: row.arch },
      {
        onSuccess: () => toast.success(t('systemUpdate.workerAssetCached', 'Worker 二进制已预缓存')),
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.workerAssetCacheFailed', 'Worker 二进制预缓存失败'))),
      },
    )
  }

  return (
    <section role="region" aria-label={title} className="rounded-lg border">
      <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
        <div>
          <h3 className="text-sm font-semibold">{title}</h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t('systemUpdate.workerAssetsSubtitle', 'CP 本地缓存状态，按节点平台聚合。')}
          </p>
        </div>
        {assets.isFetching && <RefreshCw className="size-4 animate-spin text-muted-foreground" />}
      </div>

      {assets.isError && (
        <div className="mx-4 mt-3 flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          <span>{errMsg(assets.error, t('systemUpdate.workerAssetsLoadFailed', 'Worker 二进制缓存状态加载失败'))}</span>
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
              <WorkerAssetTableRow
                key={row.key}
                row={row}
                pending={busyKey === row.key}
                onCache={doCache}
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

/** 单个平台的 Worker 缓存状态行。 */
function WorkerAssetTableRow({
  row,
  pending,
  onCache,
}: {
  row: WorkerAssetRow
  pending: boolean
  onCache: (row: WorkerAssetRow) => void
}) {
  const { t } = useTranslation()

  return (
    <TableRow>
      <TableCell className="whitespace-nowrap font-mono text-xs">{row.os}/{row.arch}</TableCell>
      <TableCell className="whitespace-nowrap font-mono text-xs">{row.version || '-'}</TableCell>
      <TableCell><WorkerAssetCacheBadge cached={row.cached} /></TableCell>
      <TableCell className="min-w-48 max-w-80 break-all font-mono text-[11px]">{row.sha256 || '-'}</TableCell>
      <TableCell className="whitespace-nowrap font-mono text-xs">{formatCacheBytes(row.size)}</TableCell>
      <TableCell className="whitespace-nowrap font-mono text-xs">{formatWorkerAssetTime(row.cachedAt)}</TableCell>
      <TableCell className={row.lastError ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
        {row.lastError || '-'}
      </TableCell>
      <TableCell className="text-right">
        <Button size="sm" variant="ghost" className="h-7 px-2" disabled={pending} onClick={() => onCache(row)}>
          {pending ? <RefreshCw className="size-4 animate-spin" /> : <Download className="size-4" />}
          {t('systemUpdate.cacheWorkerAsset', '预缓存')}
        </Button>
      </TableCell>
    </TableRow>
  )
}

/** Worker 缓存命中状态徽章。 */
function WorkerAssetCacheBadge({ cached }: { cached: boolean }) {
  const { t } = useTranslation()
  if (cached) {
    return <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">{t('systemUpdate.workerAssetCachedState', '已缓存')}</Badge>
  }
  return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.workerAssetMissingState', '未缓存')}</Badge>
}

/** 单个节点行：版本对比 + 升级 + 回滚（有备份时）。 */
function NodeRow({ node, latest, disabled, onUpgraded }: { node: ComponentStatus; latest: string; disabled: boolean; onUpgraded: () => void }) {
  const { t } = useTranslation()
  const upgrade = useUpgradeNode()
  const rollback = useRollbackNode()
  const [confirm, setConfirm] = useState(false)
  const [confirmRollback, setConfirmRollback] = useState(false)
  const hasBackup = !!node.backupVersion

  const doUpgrade = () => {
    setConfirm(false)
    upgrade.mutate(
      { nodeId: node.nodeId! },
      {
        onSuccess: (ack) => {
          toast.success(t('systemUpdate.nodeUpgraded', '节点已升级（{{from}} → {{to}}）', { from: ack.fromVersion, to: ack.toVersion }))
          onUpgraded()
        },
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.nodeUpgradeFailed', '节点升级失败'))),
      },
    )
  }

  const doRollback = () => {
    setConfirmRollback(false)
    rollback.mutate(
      { nodeId: node.nodeId! },
      {
        onSuccess: (ack) => {
          toast.success(t('systemUpdate.nodeRolledBack', '节点已回滚（{{from}} → {{to}}）', { from: ack.fromVersion, to: ack.toVersion || node.backupVersion }))
          onUpgraded()
        },
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.nodeRollbackFailed', '节点回滚失败'))),
      },
    )
  }

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
        <VersionBadge current={node.currentVersion} latest={latest} updateAvailable={node.updateAvailable} artifactAvailable={node.artifactAvailable} offline={!node.online} />
      </TableCell>
      <TableCell className="text-right">
        <div className="flex items-center justify-end gap-1">
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2"
            disabled={!node.online || !hasBackup || rollback.isPending || disabled}
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
            disabled={!node.updateAvailable || upgrade.isPending || disabled}
            onClick={() => setConfirm(true)}
          >
            <ArrowUpCircle className="size-4" />
            {t('systemUpdate.upgrade', '升级')}
          </Button>
        </div>

        <DangerConfirm
          open={confirm}
          title={t('systemUpdate.nodeUpgradeConfirm', '确定升级该节点？')}
          description={t('systemUpdate.nodeUpgradeConfirmDesc', '将令该节点下载新版 Worker、sha256 校验后替换并重启。daemon 模式下运行中的游戏服不掉。')}
          scope="platform"
          confirmLabel={t('systemUpdate.upgrade', '升级')}
          onConfirm={doUpgrade}
          onCancel={() => setConfirm(false)}
        />

        <DangerConfirm
          open={confirmRollback}
          title={t('systemUpdate.nodeRollbackConfirm', '确定回滚该节点？')}
          description={t('systemUpdate.nodeRollbackConfirmDesc', '将令该节点换回升级前备份（v{{v}}）、sha256 校验后替换并重启 Worker。daemon 模式下运行中的游戏服不掉。', { v: node.backupVersion })}
          scope="platform"
          confirmLabel={t('systemUpdate.rollback', '回滚')}
          onConfirm={doRollback}
          onCancel={() => setConfirmRollback(false)}
        />
      </TableCell>
    </TableRow>
  )
}

/** 版本对比徽章：已最新 / 可升级 / 无制品 / 离线。 */
function VersionBadge({
  current,
  latest,
  updateAvailable,
  artifactAvailable,
  offline,
}: {
  current: string
  latest: string
  updateAvailable: boolean
  artifactAvailable: boolean
  offline?: boolean
}) {
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

/** 全网升级进度面板：聚合计数 + 逐节点状态。 */
function RolloutPanel({ rollout }: { rollout: Rollout }) {
  const { t } = useTranslation()
  return (
    <div className="rounded-lg border p-4 space-y-3">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <h2 className="text-lg font-semibold">{t('systemUpdate.rolloutTitle', '全网升级进度')}</h2>
        <div className="flex items-center gap-2">
          {rollout.phase && <RolloutPhaseBadge phase={rollout.phase} />}
          <RolloutStateBadge state={rollout.state} />
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
                <TableCell><RolloutNodeBadge state={n.state} /></TableCell>
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

/** rollout 编排阶段徽章（FR-155）：金丝雀 / 滚动 / 已中止 / 已完成。 */
function RolloutPhaseBadge({ phase }: { phase: string }) {
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

/** rollout 整体状态徽章。 */
function RolloutStateBadge({ state }: { state: string }) {
  const { t } = useTranslation()
  if (state === 'running') {
    return <Badge variant="outline" className="border-amber-500/50 text-amber-600"><RefreshCw className="size-3.5 animate-spin" /> {t('systemUpdate.stateRunning', '进行中')}</Badge>
  }
  if (state === 'completed') {
    return <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">{t('systemUpdate.stateCompleted', '已完成')}</Badge>
  }
  return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.stateIdle', '空闲')}</Badge>
}

/** rollout 单节点状态徽章。 */
function RolloutNodeBadge({ state }: { state: RolloutNodeState['state'] }) {
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
