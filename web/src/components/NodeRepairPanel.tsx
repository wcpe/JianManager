import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { AlertTriangle, Copy, RotateCw, ShieldCheck, Trash2 } from 'lucide-react'
import type { NodeInfo } from '@/api/nodes'
import {
  useNodeSuspects,
  useNodeOrphans,
  useReenrollNode,
  usePurgeOrphans,
  type ReenrollResult,
} from '@/api/nodeRepair'
import { Button } from '@/components/ui/button'
import DangerConfirm from '@/components/DangerConfirm'

/**
 * 坏节点修复面板（BUG-A / ADR-039 §2，FR-177 右栏分段）。
 *
 * 历史上注册按 name 锚定身份，另一台机器同名注册会覆写旧节点身份，致按 node_id 挂的
 * JDK/实例错误路由。此面板提供：① 疑似坏节点诊断（只读，标出当前节点是否疑似）；
 * ② 重新 enroll（轮换 UUID/secret，新密钥一次性回显）；③ 清理孤立 JDK/实例。
 * 后两者破坏性，走 DangerConfirm（scope=platform）+ 后端二次确认（confirm=true）+ 审计。
 * 功能未开启时端点回 404，面板优雅降级为「未开启」提示。
 */
interface NodeRepairPanelProps {
  node: NodeInfo
  /** 是否启用查询（分段打开时为 true）。 */
  active?: boolean
}

export default function NodeRepairPanel({ node, active = true }: NodeRepairPanelProps) {
  const { t } = useTranslation()
  const suspects = useNodeSuspects({ enabled: active })
  const orphans = useNodeOrphans(node.id, { enabled: active })
  const reenroll = useReenrollNode(node.id)
  const purge = usePurgeOrphans(node.id)

  const [confirmReenroll, setConfirmReenroll] = useState(false)
  const [confirmPurge, setConfirmPurge] = useState(false)
  // 重新 enroll 成功后的新身份（新 secret 仅此一次回显，需复制保存）。
  const [issued, setIssued] = useState<ReenrollResult | null>(null)

  // 功能未开启：suspects 端点 404（repairSvc 未注入）。两个只读查询同源，任一 404 即视为未开启。
  const disabled =
    (suspects.error as { response?: { status?: number } })?.response?.status === 404 ||
    (orphans.error as { response?: { status?: number } })?.response?.status === 404

  const selfSuspect = (suspects.data ?? []).find((s) => s.node.id === node.id)

  const copySecret = (secret: string) => {
    navigator.clipboard?.writeText(secret).then(
      () => toast.success(t('nodeRepair.secretCopied')),
      () => toast.error(t('common.copyFailed')),
    )
  }

  if (disabled) {
    return (
      <div className="rounded-md border bg-muted/30 px-3 py-6 text-center text-sm text-muted-foreground">
        {t('nodeRepair.unavailable')}
      </div>
    )
  }

  return (
    <div className="space-y-3">
      {/* 当前节点疑似诊断：命中可疑信号则醒目提示，否则给「未见异常」绿条 */}
      {selfSuspect ? (
        <div className="space-y-1.5 rounded-md border border-status-warning/40 bg-status-warning/10 px-3 py-2 text-sm">
          <div className="flex items-center gap-2 font-medium text-status-warning">
            <AlertTriangle className="size-4 shrink-0" />
            {t('nodeRepair.selfSuspect')}
          </div>
          <ul className="list-disc space-y-0.5 pl-6 text-xs text-muted-foreground">
            {selfSuspect.reasons.map((r, i) => (
              <li key={i}>{r}</li>
            ))}
          </ul>
        </div>
      ) : (
        <div className="flex items-center gap-2 rounded-md border border-status-success/30 bg-status-success/10 px-3 py-2 text-sm text-status-success">
          <ShieldCheck className="size-4 shrink-0" />
          {t('nodeRepair.selfHealthy')}
        </div>
      )}

      {/* 孤立资源统计（修复前评估影响面） */}
      <div className="rounded-md border bg-muted/30 px-3 py-2 text-sm">
        <div className="mb-1 font-medium">{t('nodeRepair.orphanTitle')}</div>
        {orphans.isLoading ? (
          <p className="text-xs text-muted-foreground">{t('common.loading')}</p>
        ) : (
          <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
            <span>
              {t('nodeRepair.orphanJdk')}: <span className="font-mono text-foreground">{orphans.data?.jdkCount ?? 0}</span>
            </span>
            <span>
              {t('nodeRepair.orphanInstance')}:{' '}
              <span className="font-mono text-foreground">{orphans.data?.instanceCount ?? 0}</span>
            </span>
          </div>
        )}
      </div>

      {/* 重新 enroll 后回显的新身份（一次性） */}
      {issued && (
        <div className="space-y-1.5 rounded-md border border-primary/40 bg-primary/5 px-3 py-2 text-sm">
          <div className="font-medium text-primary">{t('nodeRepair.reenrollDone')}</div>
          <p className="text-xs text-muted-foreground">{t('nodeRepair.reenrollDoneHint')}</p>
          <div className="flex items-center gap-2">
            <code className="flex-1 truncate rounded bg-background px-2 py-1 font-mono text-xs" title={issued.newSecret}>
              {issued.newSecret}
            </code>
            <Button type="button" variant="outline" size="sm" onClick={() => copySecret(issued.newSecret)}>
              <Copy className="size-3.5" />
              {t('nodeRepair.copySecret')}
            </Button>
          </div>
        </div>
      )}

      {/* 破坏性操作 */}
      <div className="space-y-2 rounded-md border px-3 py-2.5">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="text-sm font-medium">{t('nodeRepair.reenroll')}</div>
            <p className="text-xs text-muted-foreground">{t('nodeRepair.reenrollHint')}</p>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="shrink-0"
            disabled={reenroll.isPending}
            onClick={() => setConfirmReenroll(true)}
          >
            <RotateCw className="size-3.5" />
            {t('nodeRepair.reenrollAction')}
          </Button>
        </div>
        <div className="flex items-center justify-between gap-3 border-t pt-2.5">
          <div className="min-w-0">
            <div className="text-sm font-medium">{t('nodeRepair.purge')}</div>
            <p className="text-xs text-muted-foreground">{t('nodeRepair.purgeHint')}</p>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="shrink-0 text-destructive hover:text-destructive"
            disabled={purge.isPending}
            onClick={() => setConfirmPurge(true)}
          >
            <Trash2 className="size-3.5" />
            {t('nodeRepair.purgeAction')}
          </Button>
        </div>
      </div>

      <DangerConfirm
        open={confirmReenroll}
        title={t('nodeRepair.reenrollConfirmTitle', { name: node.name })}
        description={t('nodeRepair.reenrollConfirmDesc')}
        confirmLabel={t('nodeRepair.reenrollAction')}
        scope="platform"
        onConfirm={() => {
          setConfirmReenroll(false)
          reenroll.mutate(undefined, {
            onSuccess: (res) => {
              setIssued(res)
              toast.success(t('nodeRepair.reenrollDone'))
            },
            onError: (err: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(err.response?.data?.message || t('nodeRepair.reenrollFailed')),
          })
        }}
        onCancel={() => setConfirmReenroll(false)}
      />

      <DangerConfirm
        open={confirmPurge}
        title={t('nodeRepair.purgeConfirmTitle', { name: node.name })}
        description={t('nodeRepair.purgeConfirmDesc', {
          jdk: orphans.data?.jdkCount ?? 0,
          instance: orphans.data?.instanceCount ?? 0,
        })}
        confirmLabel={t('nodeRepair.purgeAction')}
        confirmText={node.name}
        scope="platform"
        onConfirm={() => {
          setConfirmPurge(false)
          purge.mutate(undefined, {
            onSuccess: (res) =>
              toast.success(t('nodeRepair.purgeDone', { jdk: res.jdkDeleted, instance: res.instancesPurged })),
            onError: (err: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(err.response?.data?.message || t('nodeRepair.purgeFailed')),
          })
        }}
        onCancel={() => setConfirmPurge(false)}
      />
    </div>
  )
}
