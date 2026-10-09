import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useNodeProbeVersion,
  useServerProbeCatalog,
  useSetNodeProbeVersion,
} from '@/api/artifactVersions'
import NodeProbeVersionPanel from '@/components/views/nodes/NodeProbeVersionPanel'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 节点默认探针版本页签外壳（ADR-097 b 范式）。
 *
 * 两个查询（制品目录 + 当前选择）与保存 mutation 都在这里；受控视图只负责渲染
 * 与把用户选中的版本号上报上来。制品目录对象整体注入——受控视图声明的是它用到的
 * 子集结构，故无需为取类型而把制品库的整套 API 类型迁进包。
 */
export default function NodeProbeVersionTab({ nodeId }: { nodeId: number }) {
  const { t } = useTranslation()
  const { data: catalog, isLoading: catalogLoading, isError: catalogError } = useServerProbeCatalog({ enabled: true })
  const { data: selected, isLoading: selectionLoading, isError: selectionError } = useNodeProbeVersion(nodeId, { enabled: true })
  const update = useSetNodeProbeVersion(nodeId)

  return (
    <NodeProbeVersionPanel
      catalog={catalog}
      versionId={selected?.versionId ?? 0}
      isLoading={catalogLoading || selectionLoading}
      // 加载完成后仍无目录/选择，即视为错误态（与迁移前 `!catalog || !selected` 同义）。
      isError={catalogError || selectionError || !catalog || !selected}
      saving={update.isPending}
      onSave={async (versionId) => {
        try {
          await update.mutateAsync(versionId)
          toast.success(t('probe.nodeVersionSaved'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('probe.nodeVersionSaveFailed')))
          return false
        }
      }}
    />
  )
}
