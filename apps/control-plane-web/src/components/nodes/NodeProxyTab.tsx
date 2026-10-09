import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useNodeProxy, useUpdateNodeProxy } from '@/api/nodes'
import NodeProxyPanel from '@/components/views/nodes/NodeProxyPanel'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 节点出站代理页签外壳（ADR-097 b 范式）。
 *
 * 取数与保存都留在这里：请求发往何处、成功提示什么文案、失败如何取后端消息，
 * 都是应用侧策略。受控视图只把用户填好的代理配置上报上来。
 */
export default function NodeProxyTab({ nodeId }: { nodeId: number }) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useNodeProxy(nodeId, { enabled: true })
  const update = useUpdateNodeProxy(nodeId)

  return (
    <NodeProxyPanel
      data={data}
      isLoading={isLoading}
      isError={isError}
      saving={update.isPending}
      onSave={async (body) => {
        try {
          await update.mutateAsync(body)
          toast.success(t('nodeProxy.saved'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('nodeProxy.saveFailed')))
          return false
        }
      }}
    />
  )
}
