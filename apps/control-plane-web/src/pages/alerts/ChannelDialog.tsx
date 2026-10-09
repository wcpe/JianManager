import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ChannelDialogView } from '@/components/views/alerts/ChannelDialogView'
import { useCreateAlertChannel, useUpdateAlertChannel, type AlertChannelInfo } from '@/api/alerts'
import { QQBindDialog } from './QQBindDialog'

interface ChannelDialogProps {
  /** 编辑目标；null 表示创建。 */
  channel: AlertChannelInfo | null
  onClose: () => void
}

/**
 * 通知通道创建/编辑对话框（取数容器）。
 *
 * 表单状态、字段校验与渲染已回迁应用侧（原 ADR-097 迁包已撤销）；此处只保留
 * 创建/更新 mutation 调用与 QQ 扫码弹窗绑定，视图侧经回调注入。
 */
export function ChannelDialog({ channel, onClose }: ChannelDialogProps) {
  const { t } = useTranslation()
  const create = useCreateAlertChannel()
  const update = useUpdateAlertChannel()

  return (
    <ChannelDialogView
      channel={channel}
      onClose={onClose}
      submitting={create.isPending || update.isPending}
      onSubmit={async (body) => {
        if (channel) await update.mutateAsync({ id: channel.id, ...body })
        else await create.mutateAsync(body)
      }}
      onBindFilled={() => toast.success(t('alerts.qqBindDone'))}
      renderBindDialog={({ onClose: closeBind, onBound }) => (
        <QQBindDialog onClose={closeBind} onBound={onBound} />
      )}
    />
  )
}
