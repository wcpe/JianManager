import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Camera, RotateCcw, Trash2 } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import DangerConfirm from '@/components/DangerConfirm'
import {
  isSnapshotRollable,
  useCreateSnapshot,
  useDeleteSnapshot,
  useInstanceSnapshots,
  useRollbackSnapshot,
  type InstanceSnapshot,
} from '@/api/snapshots'
import { useInstance } from '@/api/instances'
import { usePermissionsStore } from '@/stores/permissions'

interface SnapshotPanelProps {
  /** 实例 DB ID。 */
  instanceId: number
}

/**
 * 实例整机快照面板（FR-466）：一次快照 = 一个时间点语义的整机可回滚点。
 *
 * 与「备份」分区的边界：备份是**归档**（可全量/增量、可存远端、面向长期留存），
 * 快照是**时间点**（一律全量、自包含、面向「一键回到某时刻」）。此处只列快照并提供
 * 「创建 / 一键回滚（二次确认）/ 删除（二次确认）」，底层归档与回放复用同一套备份通道。
 *
 * 回滚的关键安全语义（后端强制，前端如实呈现）：回滚会**先自动创建一条「回滚前」快照**
 * ——所以误点回滚是可逆的；目标快照若与当前二进制不一致，只提示不替换可执行文件。
 *
 * 权限与状态门禁（与后端 RBAC / 守卫同口径，前端只做提前提示）：
 * - 写快照/回滚需 `instance.write`；
 * - **删除快照需 `instance.delete`**（它连带删除底层全量归档，后端权限门槛高于回滚）。
 */
export default function SnapshotPanel({ instanceId }: SnapshotPanelProps) {
  const { t } = useTranslation()
  // 四态齐全：loading / error / 空 / 有数据。**绝不能用 `= []` 默认值吞掉错误**——
  // 那会把「读失败」渲染成「暂无快照」，让运维以为数据没丢（与事实相反）。
  const { data: snapshots, isLoading, isError, refetch } = useInstanceSnapshots(instanceId)
  // 实例状态用于运行态提示（快照创建允许运行态，但产出可能世界文件不一致）。
  const { data: instance } = useInstance(instanceId)
  const createSnapshot = useCreateSnapshot(instanceId)
  const rollbackSnapshot = useRollbackSnapshot(instanceId)
  const deleteSnapshot = useDeleteSnapshot(instanceId)
  const [name, setName] = useState('')
  const [pendingRollback, setPendingRollback] = useState<InstanceSnapshot | null>(null)
  const [pendingDelete, setPendingDelete] = useState<InstanceSnapshot | null>(null)

  // 与 InstanceConsolePage 同范式的前端门禁（平台管理员 hasPerm 恒 true）。
  const canWrite = usePermissionsStore((s) => s.hasPerm('instance.write'))
  const canDelete = usePermissionsStore((s) => s.hasPerm('instance.delete'))

  const rows = snapshots ?? []
  const instanceLive = !!instance && ['STARTING', 'RUNNING', 'STOPPING'].includes(instance.status)

  return (
    <Panel
      data-testid="snapshot-panel"
      title={t('serverConsole.snapshotTitle')}
      icon={<Camera className="size-3.5" />}
      bodyClassName="p-2"
      actions={
        <div className="flex items-center gap-2">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('serverConsole.snapshotNamePlaceholder')}
            aria-label={t('serverConsole.snapshotNameLabel')}
            className="h-8 w-40 rounded-md border bg-transparent px-2 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
          <Button
            size="sm"
            disabled={createSnapshot.isPending || !canWrite}
            title={!canWrite ? t('serverConsole.snapshotWriteDenied') : undefined}
            onClick={() => {
              createSnapshot.mutate(name, { onSuccess: () => setName('') })
            }}
          >
            <Camera className="size-3.5" />
            {t('serverConsole.snapshotCreate')}
          </Button>
        </div>
      }
    >
      <p className="px-0.5 pb-1.5 text-[11px] text-muted-foreground">{t('serverConsole.snapshotHint')}</p>
      {/* W-11：运行态打快照**允许**（后端只在 note 里标注），但产出的世界文件可能不一致——
          必须**在创建前**讲清，而不是等用户回滚后才发现列表里那行 note。
          同屏的「备份恢复」在运行态是禁用的，这句提示同时解释了两者语义差异。 */}
      {instanceLive && (
        <p data-testid="snapshot-running-warning" className="px-0.5 pb-1.5 text-[11px] text-status-warning">
          {t('serverConsole.snapshotRunningWarning')}
        </p>
      )}
      {isLoading ? (
        <p className="px-0.5 py-1 text-[11px] text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <div data-testid="snapshot-error" className="flex items-center gap-2 px-0.5 py-1">
          <p className="text-[11px] text-status-danger">{t('serverConsole.snapshotLoadFailed')}</p>
          <Button type="button" variant="outline" size="sm" onClick={() => void refetch()}>
            {t('common.refresh')}
          </Button>
        </div>
      ) : rows.length === 0 ? (
        <p data-testid="snapshot-empty" className="px-0.5 py-1 text-[11px] text-muted-foreground">
          {t('serverConsole.snapshotEmpty')}
        </p>
      ) : (
        <ul data-testid="snapshot-list" className="space-y-1.5">
          {rows.map((snap) => (
            <SnapshotRow
              key={snap.id}
              snapshot={snap}
              // 文案只出现在**被回滚的那一行**；其余行只是被禁用（避免误读成「很多个都在回滚」）。
              rollingBack={rollbackSnapshot.isPending && pendingRollback?.id === snap.id}
              // 互斥是全局的：任一快照回滚在途，所有行的回滚按钮都禁用，防止并发提交第二个破坏性操作。
              rollbackInFlight={rollbackSnapshot.isPending}
              rollbackDisabled={!canWrite}
              rollbackDisabledTitle={!canWrite ? t('serverConsole.snapshotWriteDenied') : undefined}
              deleteDisabled={!canDelete}
              deleteDisabledTitle={!canDelete ? t('serverConsole.snapshotDeleteDenied') : undefined}
              deleting={deleteSnapshot.isPending && pendingDelete?.id === snap.id}
              onRollback={() => setPendingRollback(snap)}
              onDelete={() => setPendingDelete(snap)}
            />
          ))}
        </ul>
      )}

      {pendingRollback && (
        <DangerConfirm
          open
          title={t('serverConsole.snapshotRollbackConfirmTitle')}
          description={t('serverConsole.snapshotRollbackConfirmBody', { name: pendingRollback.name })}
          confirmLabel={t('serverConsole.snapshotRollback')}
          // W-04：与 CP 自身升级/回滚（SystemUpdatePage 用 confirmText={latest}）同口径，
          // 逐字输入快照名 = 后端的 confirmName 要素，让「指名确认」真正落在 UI 上。
          confirmText={pendingRollback.name}
          scope="group"
          pending={rollbackSnapshot.isPending}
          /* W-02：**不在 onConfirm 里清 pendingRollback**。它同时是「进行中」判定依据
             （`rollbackSnapshot.isPending && pendingRollback !== null`），同步清空会让
             进行中态恒 false —— 「回滚中…」永不渲染、其他快照行的回滚按钮在途期间仍可点。
             正确时机是 onSettled（成功或失败都关弹窗、解除互斥），与备份恢复的能力范式一致。 */
          onConfirm={() =>
            rollbackSnapshot.mutate(pendingRollback.id, { onSettled: () => setPendingRollback(null) })
          }
          onCancel={() => setPendingRollback(null)}
        />
      )}

      {pendingDelete && (
        <DangerConfirm
          open
          title={t('serverConsole.snapshotDeleteConfirmTitle', { name: pendingDelete.name })}
          // 删除的破坏面比回滚大：连带删除底层全量归档，且不可撤销（权限门槛亦更高）。
          description={t('serverConsole.snapshotDeleteConfirmBody')}
          confirmLabel={t('common.delete')}
          confirmText={pendingDelete.name}
          scope="group"
          pending={deleteSnapshot.isPending}
          onConfirm={() => deleteSnapshot.mutate(pendingDelete.id, { onSettled: () => setPendingDelete(null) })}
          onCancel={() => setPendingDelete(null)}
        />
      )}
    </Panel>
  )
}

/** kind → i18n 键（仅覆盖后端已定义枚举；未知值回退原值，见 kindLabel）。 */
const KIND_KEY: Record<string, string> = {
  manual: 'serverConsole.snapshotKindManual',
  pre_rollback: 'serverConsole.snapshotKindPreRollback',
  scheduled: 'serverConsole.snapshotKindScheduled',
}

/** 快照类型本地化（**缺失时回退原值**，与 stateLabel 策略一致）。 */
function kindLabel(t: (key: string) => string, kind: string): string {
  const key = KIND_KEY[kind]
  if (!key) return kind
  const label = t(key)
  return label === key ? kind : label
}

function SnapshotRow({
  snapshot,
  rollingBack,
  rollbackInFlight,
  rollbackDisabled,
  rollbackDisabledTitle,
  deleteDisabled,
  deleteDisabledTitle,
  deleting,
  onRollback,
  onDelete,
}: {
  /** 快照行数据。 */
  snapshot: InstanceSnapshot
  /** **本行**是否正在被回滚（决定「回滚中…」文案是否出现在这一行）。 */
  rollingBack: boolean
  /** 是否有任一快照的回滚在途（全局互斥：在途期间所有行的回滚按钮都禁用）。 */
  rollbackInFlight: boolean
  /** 是否因权限不足禁用回滚。 */
  rollbackDisabled: boolean
  /** 权限不足时的 title 说明。 */
  rollbackDisabledTitle?: string
  /** 是否因权限不足禁用删除（后端要求 `instance.delete`，门槛高于回滚）。 */
  deleteDisabled: boolean
  /** 权限不足时的 title 说明。 */
  deleteDisabledTitle?: string
  /** 本行删除是否在途。 */
  deleting: boolean
  /** 点击回滚（仅打开二次确认，不直接提交）。 */
  onRollback: () => void
  /** 点击删除（仅打开二次确认，不直接提交）。 */
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const rollable = isSnapshotRollable(snapshot)
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border bg-muted/50 px-2.5 py-2 text-xs">
      <span className="font-medium">{snapshot.name}</span>
      <span
        data-testid="snapshot-kind"
        data-kind={snapshot.kind}
        className={
          snapshot.kind === 'pre_rollback'
            ? 'rounded-full bg-status-warning/10 px-2 py-0.5 text-[11px] text-status-warning'
            : 'rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground'
        }
      >
        {kindLabel(t, snapshot.kind)}
      </span>
      <span className="font-mono text-muted-foreground">{new Date(snapshot.createdAt).toLocaleString()}</span>
      <span className="text-muted-foreground">{stateLabel(t, snapshot.state)}</span>
      {snapshot.sizeMb > 0 && <span className="font-mono text-muted-foreground">{snapshot.sizeMb.toFixed(1)} MB</span>}
      {snapshot.binaryName && (
        <span className="font-mono text-muted-foreground" title={snapshot.binarySha256}>
          {t('serverConsole.snapshotBinary', { name: snapshot.binaryName })}
        </span>
      )}
      {snapshot.note && <span className="w-full text-[11px] text-status-warning">{snapshot.note}</span>}
      {snapshot.state === 'failed' && snapshot.failureReason && (
        <span className="w-full text-[11px] text-status-danger">{snapshot.failureReason}</span>
      )}
      {/* B-1：底链缺失时状态仍可能是 completed，但实际不可回滚——必须显式说出原因，
          而不是让运维点下去才失败（原缺陷正是「列表显示可回滚、点击报 record not found」）。 */}
      {snapshot.notRollableReason && (
        <span data-testid="snapshot-not-rollable" className="w-full text-[11px] text-status-danger">
          {snapshot.notRollableReason}
        </span>
      )}
      <div className="ml-auto flex items-center gap-1">
        {rollingBack && <span className="text-[11px] text-muted-foreground">{t('serverConsole.snapshotRollingBack')}</span>}
        {deleting && <span className="text-[11px] text-muted-foreground">{t('common.deleting')}</span>}
        <Button
          size="sm"
          variant="outline"
          disabled={!rollable || rollbackInFlight || rollbackDisabled || deleting}
          title={rollbackDisabledTitle}
          onClick={onRollback}
        >
          <RotateCcw className="size-3.5" />
          {t('serverConsole.snapshotRollback')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          disabled={deleting || rollbackInFlight || deleteDisabled}
          title={deleteDisabledTitle}
          onClick={onDelete}
          aria-label={t('common.delete')}
        >
          <Trash2 className="size-3.5" />
        </Button>
      </div>
    </li>
  )
}

/** 快照状态本地化（缺失时回退原值，便于后端新增状态时前端不白屏）。 */
function stateLabel(t: (key: string) => string, state: string): string {
  const key = `serverConsole.snapshotState.${state}`
  const label = t(key)
  return label === key ? state : label
}
