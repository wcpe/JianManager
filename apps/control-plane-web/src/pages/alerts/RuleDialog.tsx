import { useTranslation } from 'react-i18next'
import { useNodes } from '@/api/nodes'
import { InstancePicker } from '@/components/InstancePicker'
import { useCreateAlertRule, useUpdateAlertRule, type AlertRuleInfo, type AlertChannelInfo } from '@/api/alerts'
import { RuleDialogView } from '@/components/views/alerts/RuleDialogView'

interface RuleDialogProps {
  /** 编辑目标；null 表示创建。 */
  rule: AlertRuleInfo | null
  channels: AlertChannelInfo[]
  onClose: () => void
}

/**
 * 告警规则创建/编辑对话框（取数容器）。
 *
 * 表单状态、字段校验与渲染已回迁应用侧（原 ADR-097 迁包已撤销）；此处只保留
 * 规则 mutation、节点列表取数与实例选择器（服务端搜索）绑定。
 */
export function RuleDialog({ rule, channels, onClose }: RuleDialogProps) {
  const { t } = useTranslation()
  const create = useCreateAlertRule()
  const update = useUpdateAlertRule()
  const { data: nodes } = useNodes()

  return (
    <RuleDialogView
      rule={rule}
      channels={channels}
      nodes={nodes}
      onClose={onClose}
      submitting={create.isPending || update.isPending}
      onSubmit={async (payload) => {
        if (payload.mode === 'update') await update.mutateAsync(payload.body)
        else await create.mutateAsync(payload.body)
      }}
      renderInstancePicker={({ value, onChange }) => (
        <InstancePicker
          value={value}
          onChange={onChange}
          allowAll
          allLabel={t('alerts.allInstances')}
          className="mt-1"
        />
      )}
    />
  )
}
