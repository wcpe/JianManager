import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Copy, RotateCw, ShieldCheck, Trash2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { copyToClipboard } from '@/lib/shared/clipboard'

/**
 * 疑似坏节点条目（本组件所需的最小结构）。
 *
 * 只声明用到的 `node.{id,name}` 与 `reasons`，故外壳直接传 `@/api/nodeRepair` 的
 * 返回项会结构兼容，无需把该 API 类型迁进包。
 */
export interface NodeSuspectView {
  node: { id: number; name: string }
  /** 命中可疑信号的原因列表。 */
  reasons: string[]
}

/** 孤立资源统计（同上）。 */
export interface NodeOrphansView {
  jdkCount: number
  instanceCount: number
}

/** 重新 enroll 的结果（同上，本组件只需一次性回显的新密钥）。 */
export interface ReenrollResultView {
  newSecret: string
}

/**
 * 坏节点修复面板（BUG-A / ADR-039 §2，FR-177 右栏分段）。
 *
 * 历史上注册按 name 锚定身份，另一台机器同名注册会覆写旧节点身份，致按 node_id 挂的
 * JDK/实例错误路由。此面板提供：① 疑似坏节点诊断（只读，标出当前节点是否疑似）；
 * ② 重新 enroll（轮换 UUID/secret，新密钥一次性回显）；③ 清理孤立 JDK/实例。
 * 后两者破坏性，走 DangerConfirm（scope=platform）+ 后端二次确认（confirm=true）+ 审计。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast。
 * - 诊断数据与统计经 props 注入（原先的 `node: NodeInfo` 收缩为 `nodeId + nodeName`
 *   两个本组件真正用到的字段）；
 * - 两个破坏性动作以回调上报，且 `onReenroll` **回传新身份**——一次性密钥必须由
 *   发起方交回给视图才能回显，这是本组件唯一需要读取动作结果的地方；
 * - 原先的 404 降级判定移出（需要读 error.response.status），改由外壳注入 `unavailable`。
 */
export interface NodeRepairPanelProps {
  /** 当前节点 id（用于判定「本节点是否疑似」）。 */
  nodeId: number
  /** 当前节点名（用于二次确认文案与逐字校验）。 */
  nodeName: string
  /** 疑似坏节点列表；外壳取数后注入。 */
  suspects?: NodeSuspectView[]
  /** 孤立资源统计。 */
  orphans?: NodeOrphansView
  /** 统计加载态（渲染骨架）。 */
  orphansLoading?: boolean
  /** 功能未开启（端点 404）：整块降级为提示文案。 */
  unavailable?: boolean
  /** 重新 enroll 在途。 */
  reenrolling?: boolean
  /** 清理孤立资源在途。 */
  purging?: boolean
  /** 重新 enroll。成功回传新身份供一次性回显；失败回 null。 */
  onReenroll: () => Promise<ReenrollResultView | null>
  /** 清理孤立资源。返回是否成功。 */
  onPurge: () => Promise<boolean>
  /** 复制密钥的结果上报（由外壳决定提示文案）。 */
  onCopyResult?: (ok: boolean) => void
}

export default function NodeRepairPanel({
  nodeId,
  nodeName,
  suspects,
  orphans,
  orphansLoading = false,
  unavailable = false,
  reenrolling = false,
  purging = false,
  onReenroll,
  onPurge,
  onCopyResult,
}: NodeRepairPanelProps) {
  const { t } = useTranslation()

  const [confirmReenroll, setConfirmReenroll] = useState(false)
  const [confirmPurge, setConfirmPurge] = useState(false)
  // 重新 enroll 成功后的新身份（新 secret 仅此一次回显，需复制保存）。
  const [issued, setIssued] = useState<ReenrollResultView | null>(null)

  if (unavailable) {
    return (
      <div className="rounded-md border bg-muted/30 px-3 py-6 text-center text-sm text-muted-foreground">
        {t('nodeRepair.unavailable')}
      </div>
    )
  }

  const selfSuspect = (suspects ?? []).find((s) => s.node.id === nodeId)

  const copySecret = async (secret: string) => {
    const ok = await copyToClipboard(secret)
    onCopyResult?.(ok)
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
        {orphansLoading ? (
          /* 骨架占位：对应两个孤立资源统计项（文本行高 ≈ h-4）。 */
          <div className="flex flex-wrap gap-4">
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-4 w-28" />
          </div>
        ) : (
          <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
            <span>
              {t('nodeRepair.orphanJdk')}: <span className="font-mono text-foreground">{orphans?.jdkCount ?? 0}</span>
            </span>
            <span>
              {t('nodeRepair.orphanInstance')}:{' '}
              <span className="font-mono text-foreground">{orphans?.instanceCount ?? 0}</span>
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
            <Button type="button" variant="outline" size="sm" onClick={() => void copySecret(issued.newSecret)}>
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
            disabled={reenrolling}
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
            disabled={purging}
            onClick={() => setConfirmPurge(true)}
          >
            <Trash2 className="size-3.5" />
            {t('nodeRepair.purgeAction')}
          </Button>
        </div>
      </div>

      <DangerConfirm
        open={confirmReenroll}
        title={t('nodeRepair.reenrollConfirmTitle', { name: nodeName })}
        description={t('nodeRepair.reenrollConfirmDesc')}
        confirmLabel={t('nodeRepair.reenrollAction')}
        scope="platform"
        onConfirm={() => {
          setConfirmReenroll(false)
          void onReenroll().then((res) => {
            // 失败（null）时不覆盖上一次已回显的身份，避免把用户手上的密钥抹掉。
            if (res) setIssued(res)
          })
        }}
        onCancel={() => setConfirmReenroll(false)}
      />

      <DangerConfirm
        open={confirmPurge}
        title={t('nodeRepair.purgeConfirmTitle', { name: nodeName })}
        description={t('nodeRepair.purgeConfirmDesc', {
          jdk: orphans?.jdkCount ?? 0,
          instance: orphans?.instanceCount ?? 0,
        })}
        confirmLabel={t('nodeRepair.purgeAction')}
        confirmText={nodeName}
        scope="platform"
        onConfirm={() => {
          setConfirmPurge(false)
          void onPurge()
        }}
        onCancel={() => setConfirmPurge(false)}
      />
    </div>
  )
}
