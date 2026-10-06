import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useUpdateInstance } from '@/api/instances'
import InstanceTagsDialogView from '@jianmanager/ui/components/views/instances/InstanceTagsDialog'

/**
 * 实例标签编辑器的应用接线层（ADR-097 b 范式）。
 *
 * 编辑器本体已迁入组件库并受控；本层只做一件事：把合并好的标签数组持久化并提示。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function InstanceTagsDialog({
  instanceId,
  instanceName,
  tags,
  onClose,
}: {
  instanceId: number
  instanceName: string
  /** 当前标签集合（含 env: 环境标签 + 自由标签）。 */
  tags: string[]
  onClose: () => void
}) {
  const { t } = useTranslation()
  const update = useUpdateInstance()

  return (
    <InstanceTagsDialogView
      instanceName={instanceName}
      tags={tags}
      saving={update.isPending}
      onClose={onClose}
      onSave={async (merged) => {
        try {
          await update.mutateAsync({ id: instanceId, body: { tags: merged } })
          toast.success(t('grouping.tagsSaved'))
          return true
        } catch {
          return false
        }
      }}
    />
  )
}
