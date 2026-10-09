import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useNodeSuspects,
  useNodeOrphans,
  useReenrollNode,
  usePurgeOrphans,
} from '@/api/nodeRepair'
import NodeRepairPanel from '@/components/views/nodes/NodeRepairPanel'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 坏节点修复分段外壳（ADR-097 b 范式）。
 *
 * 这里承担三件应用侧的事：两个只读查询、两个破坏性 mutation 及其提示、
 * 以及 404 降级判定（要读 `error.response.status`，故不能留在包里）。
 */
export default function NodeRepairTab({
  node,
  active = true,
}: {
  /** 节点标识与展示名——只传视图真正用到的两个字段，而非整个 NodeInfo。 */
  node: { id: number; name: string }
  active?: boolean
}) {
  const { t } = useTranslation()
  const suspects = useNodeSuspects({ enabled: active })
  const orphans = useNodeOrphans(node.id, { enabled: active })
  const reenroll = useReenrollNode(node.id)
  const purge = usePurgeOrphans(node.id)

  // 功能未开启：suspects 端点 404（repairSvc 未注入）。两个只读查询同源，任一 404 即视为未开启。
  const unavailable =
    (suspects.error as { response?: { status?: number } })?.response?.status === 404 ||
    (orphans.error as { response?: { status?: number } })?.response?.status === 404

  return (
    <NodeRepairPanel
      nodeId={node.id}
      nodeName={node.name}
      suspects={suspects.data}
      orphans={orphans.data}
      orphansLoading={orphans.isLoading}
      unavailable={unavailable}
      reenrolling={reenroll.isPending}
      purging={purge.isPending}
      onReenroll={async () => {
        try {
          const res = await reenroll.mutateAsync()
          toast.success(t('nodeRepair.reenrollDone'))
          // 新密钥只此一次回显，必须把结果交回视图。
          return res
        } catch (err) {
          toast.error(errMessage(err, t('nodeRepair.reenrollFailed')))
          return null
        }
      }}
      onPurge={async () => {
        try {
          const res = await purge.mutateAsync()
          toast.success(t('nodeRepair.purgeDone', { jdk: res.jdkDeleted, instance: res.instancesPurged }))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('nodeRepair.purgeFailed')))
          return false
        }
      }}
      onCopyResult={(ok) => {
        if (ok) toast.success(t('nodeRepair.secretCopied'))
        else toast.error(t('common.copyFailed'))
      }}
    />
  )
}
