import { useTranslation } from 'react-i18next'
import { useCreateBot } from '@/api/bots'
import { useInstance } from '@/api/instances'
import { useNode } from '@/api/nodes'
import { suggestBotServer } from '@/lib/bot-list'
import CreateBotDialogView from '@/components/views/instances/CreateBotDialog'

/**
 * 「新建 Bot」对话框的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体是受控视图（见 components/views）；本层算两样它不该自己拿的东西：归属实例名、
 * 「节点 host + 实例端口」的建议连接地址（要查实例与节点），以及创建动作。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function CreateBotDialog({
  open,
  onOpenChange,
  instanceId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 当前工作区打开的实例 id，新建 Bot 归属于它 */
  instanceId: number
}) {
  const { t } = useTranslation()
  const create = useCreateBot()
  const { data: instance } = useInstance(instanceId)
  const { data: node } = useNode(instance?.nodeId ?? 0)
  const suggested = suggestBotServer(node, instance?.serverPort)

  return (
    <CreateBotDialogView
      open={open}
      onOpenChange={onOpenChange}
      instanceName={instance?.name ?? `#${instanceId}`}
      suggestedServer={suggested.server}
      suggestedPort={suggested.port}
      creating={create.isPending}
      onCreate={async (payload) => {
        try {
          const bot = await create.mutateAsync({
            instanceId,
            name: payload.name,
            config: { server: payload.server, port: payload.port, auth: payload.auth },
            behavior: payload.behavior,
          })
          // 记录创建成功但委托 Worker 失败（如 bot 依赖未装）：交回视图显示可操作原因。
          if (bot?.status === 'error') {
            return { ok: false as const, error: bot.lastError || t('bots.delegateFailed') }
          }
          return { ok: true as const }
        } catch (err) {
          const msg =
            err instanceof Error && 'response' in err
              ? (err as { response?: { data?: { message?: string } } }).response?.data?.message
              : undefined
          return { ok: false as const, error: msg || t('bots.createFailed') }
        }
      }}
    />
  )
}
