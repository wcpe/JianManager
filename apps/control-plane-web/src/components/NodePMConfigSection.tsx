// 视图已迁至 @jianmanager/ui（ADR-097）；本层只注入配置快照与保存 mutation（受控化）。
import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'
import { NodePMConfigSection as NodePMConfigSectionView } from '@/components/views/nodes/NodePMConfigSection'
import { useNodePMConfig, useSetNodePMConfig } from '@/api/pmConfig'

interface NodePMConfigSectionProps {
  nodeId: number
  active?: boolean
}

/**
 * 节点包管理器与 registry 配置子区的取数接线层（FR-306）。
 * 保存成功/失败的回执在此给出（视图不依赖 toast 实现）。
 */
export default function NodePMConfigSection({ nodeId, active = true }: NodePMConfigSectionProps) {
  const { t } = useTranslation()
  const { data, isLoading } = useNodePMConfig(nodeId, { enabled: active })
  const save = useSetNodePMConfig(nodeId)

  return (
    <NodePMConfigSectionView
      data={data}
      isLoading={isLoading}
      saving={save.isPending}
      active={active}
      onSave={(payload) => {
        save.mutate(payload, {
          onSuccess: () => toast.success(t('pmConfig.saved', '包管理器配置已保存')),
          onError: (e: Error & { response?: { data?: { message?: string } } }) =>
            toast.error(e.response?.data?.message || t('pmConfig.saveFailed', '保存失败')),
        })
      }}
    />
  )
}
