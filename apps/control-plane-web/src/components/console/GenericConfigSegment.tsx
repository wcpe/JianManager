// 视图已迁至 @jianmanager/ui（ADR-097）；本层只注入发现结果与编辑器（受控化）。
import { GenericConfigSegment as GenericConfigSegmentView } from '@/components/views/console/GenericConfigSegment'
import { useConfigDiscover } from '@/api/configs'
import ConfigFileEditor from '@/components/config-explorer/ConfigFileEditor'

/**
 * 结构化配置编辑分段的取数接线层（FR-451 衔接，FR-449/450 复用）。
 *
 * 左栏「发现 → 选中」已入包；右侧编辑器留应用侧（`ConfigFileEditor` 含 schema 表单 /
 * 原文双模式与配置版本能力），经 renderEditor 注入。
 */
export default function GenericConfigSegment({ instanceId }: { instanceId: number }) {
  const { data, isLoading } = useConfigDiscover(instanceId)

  return (
    <GenericConfigSegmentView
      files={data?.files ?? []}
      isLoading={isLoading}
      renderEditor={({ path, onClose }) => (
        <ConfigFileEditor
          instanceId={instanceId}
          path={path}
          name={path.split('/').pop() ?? path}
          onClose={onClose}
          onAfterSave={() => {
            /* 读取查询在保存后自动失效刷新 */
          }}
          onOpenVersions={() => {
            /* 版本抽屉仅在文件配置页；此处不阻塞编辑 */
          }}
          onDirtyChange={() => {
            /* 详情页不接管切换守卫 */
          }}
        />
      )}
    />
  )
}
