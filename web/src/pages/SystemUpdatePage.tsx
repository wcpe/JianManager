import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { RefreshCw, ArrowUpCircle, ArrowDownCircle, ServerCog, AlertCircle, CheckCircle2, Clock } from 'lucide-react'
import {
  useSelfUpdateCheck,
  useRefreshSelfUpdateCheck,
  useRollout,
  useUpgradeControlPlane,
  useUpgradeNode,
  useUpgradeAll,
  useRollbackControlPlane,
  useRollbackNode,
  type ComponentStatus,
  type Rollout,
  type RolloutNodeState,
} from '@/api/selfUpdate'
import { useAuthStore } from '@/stores/auth'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import DangerConfirm from '@/components/DangerConfirm'
import { ReleaseNotes } from '@/components/ReleaseNotes'
import { formatRelativeTime } from '@/lib/relative-time'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

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

  // 进页后台静默刷新一次（缓存即显在前、live 结果随后更新）。仅触发一次，避免重渲染重复刷新。
  const autoRefreshedRef = useRef(false)
  useEffect(() => {
    if (!isPlatformAdmin || autoRefreshedRef.current) return
    autoRefreshedRef.current = true
    refresh.mutate(undefined, {
      // 后台静默：失败不弹错（保留缓存即可），仅手动点「检查更新」时才提示失败。
      onError: () => {},
    })
  }, [isPlatformAdmin, refresh])

  // rollout 在运行中时短轮询，空闲/完成后停（轮询逻辑在 hook 内）。
  const rolloutQ = useRollout()
  const rolloutRunning = rolloutQ.data?.state === 'running'

  const [confirmAll, setConfirmAll] = useState(false)

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
    upgradeAll.mutate(
      {},
      {
        onSuccess: () => {
          toast.success(t('systemUpdate.rolloutStarted', '全网升级已发起'))
          void rolloutQ.refetch()
        },
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.rolloutFailed', '发起全网升级失败'))),
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
            rolloutRunning={rolloutRunning}
            onUpgradeAll={() => setConfirmAll(true)}
            onUpgraded={() => refresh.mutate(undefined)}
          />
        </>
      )}

      {rolloutQ.data && rolloutQ.data.state !== 'idle' && <RolloutPanel rollout={rolloutQ.data} />}

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
        onConfirm={doUpgrade}
        onCancel={() => setConfirm(false)}
      />

      <DangerConfirm
        open={confirmRollback}
        title={t('systemUpdate.cpRollbackConfirm', '确定回滚 Control Plane？')}
        description={t('systemUpdate.cpRollbackConfirmDesc', '将把控制台换回升级前备份（v{{v}}）、sha256 校验后替换并平滑重启。重启期间 Web 短暂不可用，重连后即为旧版本。', { v: cp.backupVersion })}
        scope="platform"
        confirmLabel={t('systemUpdate.rollback', '回滚')}
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
  rolloutRunning,
  onUpgradeAll,
  onUpgraded,
}: {
  nodes: ComponentStatus[]
  latest: string
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
        <RolloutStateBadge state={rollout.state} />
      </div>
      <div className="text-sm text-muted-foreground flex flex-wrap gap-x-4 gap-y-1">
        <span>{t('systemUpdate.rolloutTarget', '目标版本')}：<span className="font-mono text-foreground">{rollout.targetVersion || t('systemUpdate.feedLatest', '源最新')}</span></span>
        <span>{t('systemUpdate.rolloutTotal', '共 {{n}} 个', { n: rollout.total })}</span>
        <span className="text-emerald-600">{t('systemUpdate.rolloutSucceeded', '成功 {{n}}', { n: rollout.succeeded })}</span>
        <span className="text-destructive">{t('systemUpdate.rolloutFailed', '失败 {{n}}', { n: rollout.failed })}</span>
        <span>{t('systemUpdate.rolloutPending', '待处理 {{n}}', { n: rollout.pending })}</span>
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
    default:
      return <Badge variant="outline" className="text-muted-foreground">{t('systemUpdate.nodePending', '待处理')}</Badge>
  }
}
