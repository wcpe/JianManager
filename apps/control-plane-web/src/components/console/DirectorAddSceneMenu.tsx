// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只注入 store 动作（受控化）。
import { DirectorAddSceneMenu as DirectorAddSceneMenuView } from '@/components/views/console/WorkbenchLeafParts'
import { useDirectorStore } from '@/stores/director'
import type { WorkspacePreset } from '@/lib/console/workspace-preset'

interface DirectorAddSceneMenuProps {
  /** 可选的用户预设（跨实例 + 单实例共享一份，FR-167）。 */
  userPresets: WorkspacePreset[]
  /** 触发按钮样式：default=轮廓小按钮；primary=空态主操作。 */
  variant?: 'default' | 'primary'
}

/**
 * 「添加场景」菜单的取数接线层（FR-168）。
 *
 * 导入即克隆该预设卡片为新场景，追加到缩略图条末尾，并加入状态机（cold，切到才保活）——
 * 该动作由导播台 store 提供，故在此注入。
 */
export default function DirectorAddSceneMenu({ userPresets, variant }: DirectorAddSceneMenuProps) {
  const addSceneFromPreset = useDirectorStore((s) => s.addSceneFromPreset)
  return <DirectorAddSceneMenuView userPresets={userPresets} variant={variant} onAddScene={addSceneFromPreset} />
}
