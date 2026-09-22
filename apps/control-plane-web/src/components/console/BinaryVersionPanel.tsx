import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowDownUp, Package } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import DangerConfirm from '@/components/DangerConfirm'
import { useBinaryRollback, useBinaryUpgrade, useBinaryVersion, type BinaryVersionView } from '@/api/binaryVersion'
import { useInstance } from '@/api/instances'
import { usePermissionsStore } from '@/stores/permissions'

interface BinaryVersionPanelProps {
  /** 实例 DB ID。 */
  instanceId: number
}

/** pendingChange 待二次确认的版本变更（m-1：升级/回滚都会替换实例可执行文件）。 */
type PendingChange =
  | { kind: 'upgrade'; assetId: number; label: string }
  | { kind: 'rollback'; label: string }

/**
 * 实例「二进制版本」区（FR-468）：当前版本 + 受控升级 + 一级回滚。
 *
 * 为什么需要它：搭建时版本是**隐式**解析的（Beacon 预设取制品库最新），运维既不知道
 * 「现在跑的是哪一版」，也没有升级入口——改版本只能靠自己记住旧文件名、手动换文件。
 * 本区把「当前版本 / 可升级到哪些 / 能不能回滚」一次讲清。
 *
 * 关键语义（后端强制，前端如实呈现）：
 * - 升级/回滚都要求实例**已停止**（替换正在执行的可执行文件会留下不一致窗口）；
 *   前端按实例状态**主动禁用**并给出原因 title，不把可预见的失败留给用户去撞后端 409
 *   （同页签的备份恢复已是此口径）；
 * - 重建不再重新解析版本（旧行为会静默升降级），所以「吃到新版本」必须走这里的升级；
 * - url / node_file 来源没有制品库版本，升级入口给出明确提示而非静默失败；
 * - 两个动作都会覆盖工作目录里的可执行文件，故均需**逐字确认**目标版本号/文件名
 *   （与 CP 自身升级/回滚的 `confirmText` 同口径）。
 *
 * 权限门禁：读走 `instance.read`（查询本身），写（升级/回滚）需 `instance.write`。
 */
export default function BinaryVersionPanel({ instanceId }: BinaryVersionPanelProps) {
  const { t } = useTranslation()
  // 四态齐全：loading / error / 有数据 /（无空态语义，unbound 是显式业务态）。
  // 原来只取 isLoading，请求失败后 `isLoading || !view` 恒真 → 永久停在「加载中…」。
  const { data: view, isLoading, isError, refetch } = useBinaryVersion(instanceId)
  // 实例状态：升级/回滚要求已停止，前端据此禁用（后端 409 只是最终兜底）。
  const { data: instance } = useInstance(instanceId)
  const upgrade = useBinaryUpgrade(instanceId)
  const rollback = useBinaryRollback(instanceId)
  const [target, setTarget] = useState<number | ''>('')
  const [pending, setPending] = useState<PendingChange | null>(null)

  const canWrite = usePermissionsStore((s) => s.hasPerm('instance.write'))

  const loadingPanel = (
    <Panel
      data-testid="binary-version-panel"
      title={t('serverConsole.binaryVersionTitle')}
      /* 加载占位也带 icon：否则标题栏在「加载 → 有数据」之间图标出现、高度跳动（W-20）。 */
      icon={<Package className="size-3.5" />}
    >
      <p className="px-2.5 py-1.5 text-[11px] text-muted-foreground">{t('common.loading')}</p>
    </Panel>
  )

  if (isLoading) return loadingPanel

  if (isError) {
    return (
      <Panel
        data-testid="binary-version-panel"
        title={t('serverConsole.binaryVersionTitle')}
        icon={<Package className="size-3.5" />}
      >
        <div data-testid="binary-version-error" className="flex items-center gap-2 px-2.5 py-1.5">
          <p className="text-[11px] text-status-danger">{t('serverConsole.binaryVersionLoadFailed')}</p>
          <Button type="button" variant="outline" size="sm" onClick={() => void refetch()}>
            {t('common.refresh')}
          </Button>
        </div>
      </Panel>
    )
  }

  if (!view) return loadingPanel

  const instanceLive = !!instance && ['STARTING', 'RUNNING', 'STOPPING'].includes(instance.status)
  // 禁用原因：权限 > 运行态（两个都说了才有意义，故权限优先给出）。
  const changeBlockedTitle = !canWrite
    ? t('serverConsole.binaryVersionWriteDenied')
    : instanceLive
      ? t('serverConsole.binaryVersionStopHint')
      : undefined

  return (
    <Panel
      data-testid="binary-version-panel"
      title={t('serverConsole.binaryVersionTitle')}
      icon={<Package className="size-3.5" />}
      bodyClassName="p-2.5"
      actions={
        <div className="flex items-center gap-2">
          <select
            aria-label={t('serverConsole.binaryVersionTargetLabel')}
            value={target}
            onChange={(e) => setTarget(e.target.value === '' ? '' : Number(e.target.value))}
            disabled={!view.bound || view.noLibraryVersion || instanceLive || !canWrite}
            className="h-8 rounded-md border bg-transparent px-2 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
          >
            <option value="">{t('serverConsole.binaryVersionPickTarget')}</option>
            {view.candidates.map((c) => (
              <option key={c.assetId} value={c.assetId}>
                {c.version || c.filename} (asset#{c.assetId})
              </option>
            ))}
          </select>
          <Button
            size="sm"
            disabled={target === '' || upgrade.isPending || !view.bound || instanceLive || !canWrite}
            title={changeBlockedTitle}
            onClick={() => {
              if (target === '') return
              const candidate = view.candidates.find((c) => c.assetId === target)
              setPending({
                kind: 'upgrade',
                assetId: target,
                label: candidate ? candidate.version || candidate.filename : `asset#${target}`,
              })
            }}
          >
            <ArrowDownUp className="size-3.5" />
            {t('serverConsole.binaryVersionUpgrade')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!view.hasRollback || rollback.isPending || instanceLive || !canWrite}
            title={changeBlockedTitle}
            onClick={() =>
              setPending({
                kind: 'rollback',
                label: view.previousVersion || view.previousFilename || `asset#${view.previousAssetId}`,
              })
            }
          >
            {t('serverConsole.binaryVersionRollback')}
          </Button>
        </div>
      }
    >
      <div className="space-y-1.5 text-xs">
        <p className="text-[11px] text-muted-foreground">{t('serverConsole.binaryVersionHint')}</p>
        {!view.bound ? (
          <p data-testid="binary-version-unbound" className="text-[11px] text-muted-foreground">
            {view.note ?? t('serverConsole.binaryVersionUnbound')}
          </p>
        ) : (
          <>
            <BinaryVersionSummary view={view} />
            {view.noLibraryVersion && (
              <p data-testid="binary-version-no-library" className="text-[11px] text-status-warning">
                {t('serverConsole.binaryVersionNoLibrary')}
              </p>
            )}
            {view.driftDetected && (
              <p data-testid="binary-version-drift" className="text-[11px] text-status-warning">
                {t('serverConsole.binaryVersionDrift')}
                {view.driftReason ? `：${view.driftReason}` : ''}
              </p>
            )}
            {/* M-3：磁盘内容比对是「是否真的读过盘」的显式标记——
                未校验（节点离线/文件过大/运行中）与「已核对且一致」必须区分开。
                N-4：「读过但没算成」写成独立的跳过原因，绝不并入 driftDetected。 */}
            {view.diskSha256Checked ? (
              <p data-testid="binary-version-disk-checked" className="text-[11px] text-muted-foreground">
                {t('serverConsole.binaryVersionDiskChecked')}
              </p>
            ) : (
              <p data-testid="binary-version-disk-unchecked" className="text-[11px] text-muted-foreground">
                {t('serverConsole.binaryVersionDiskUnchecked')}
              </p>
            )}
            {view.diskCheckSkippedReason && (
              <p data-testid="binary-version-disk-skipped" className="text-[11px] text-muted-foreground">
                {t('serverConsole.binaryVersionDiskSkipped')}：{view.diskCheckSkippedReason}
              </p>
            )}
            <p className="text-[11px] text-muted-foreground">{t('serverConsole.binaryVersionStopHint')}</p>
          </>
        )}
      </div>

      {pending && (
        <DangerConfirm
          open
          title={
            pending.kind === 'upgrade'
              ? t('serverConsole.binaryVersionUpgradeConfirmTitle')
              : t('serverConsole.binaryVersionRollbackConfirmTitle')
          }
          description={
            pending.kind === 'upgrade'
              ? t('serverConsole.binaryVersionUpgradeConfirmBody', { target: pending.label })
              : t('serverConsole.binaryVersionRollbackConfirmBody', { target: pending.label })
          }
          confirmLabel={
            pending.kind === 'upgrade'
              ? t('serverConsole.binaryVersionUpgrade')
              : t('serverConsole.binaryVersionRollback')
          }
          /* W-04：替换可执行文件与 CP 自身升级/回滚（SystemUpdatePage 的 confirmText）同量级，
             要求逐字输入目标版本号/文件名——单次点击不足以承担「覆盖生产可执行文件」的风险。 */
          confirmText={pending.label}
          scope="group"
          pending={upgrade.isPending || rollback.isPending}
          /* W-02 同源修正：不在 onConfirm 里清 pending —— 清空需等 onSettled，
             否则「确认中」按钮态与弹窗在途互斥都会失效。 */
          onConfirm={() => {
            const settle = () => setPending(null)
            if (pending.kind === 'upgrade') {
              upgrade.mutate(pending.assetId, { onSuccess: () => setTarget(''), onSettled: settle })
            } else {
              rollback.mutate(undefined, { onSettled: settle })
            }
          }}
          onCancel={() => setPending(null)}
        />
      )}
    </Panel>
  )
}

function BinaryVersionSummary({ view }: { view: BinaryVersionView }) {
  const { t } = useTranslation()
  return (
    <>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-muted-foreground">{t('serverConsole.binaryVersionCurrent')}</span>
        <span data-testid="binary-version-current" className="font-mono text-foreground">
          {view.currentVersion || view.currentFilename || '—'}
        </span>
        {view.currentAssetId > 0 && <span className="font-mono text-muted-foreground">asset#{view.currentAssetId}</span>}
      </div>
      {view.currentSha256 && (
        <div className="truncate font-mono text-[11px] text-muted-foreground" title={view.currentSha256}>
          sha256 {view.currentSha256.slice(0, 16)}…
        </div>
      )}
      {view.hasRollback && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <span className="text-muted-foreground">{t('serverConsole.binaryVersionPrevious')}</span>
          <span data-testid="binary-version-previous" className="font-mono text-foreground">
            {view.previousVersion || view.previousFilename || `asset#${view.previousAssetId}`}
          </span>
        </div>
      )}
    </>
  )
}
