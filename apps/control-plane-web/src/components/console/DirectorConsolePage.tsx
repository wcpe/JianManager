import { DirectorConsolePage as DirectorConsolePageImpl } from '@/components/views/console/DirectorConsolePage'
import { useDirectorStore } from '@/stores/director'
import { useWorkspaceStore } from '@/stores/workspace'
import DirectorCanvas from './DirectorCanvas'
import DirectorAddSceneMenu from './DirectorAddSceneMenu'

/**
 * 导播台页面的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层注入导播 store 的场景/状态机/轮播开关与四个动作、
 * 工作台预设，以及两个自带取数的子组件（画布与添加场景菜单）。
 * 保留同路径的默认导出与零 props，调用点无需改动。
 */
export default function DirectorConsolePage() {
  const scenes = useDirectorStore((s) => s.scenes)
  const machine = useDirectorStore((s) => s.machine)
  const carouselOn = useDirectorStore((s) => s.carouselOn)
  const carouselMs = useDirectorStore((s) => s.carouselMs)
  const activate = useDirectorStore((s) => s.activate)
  const advance = useDirectorStore((s) => s.advance)
  const setCarouselOn = useDirectorStore((s) => s.setCarouselOn)
  const removeScene = useDirectorStore((s) => s.removeScene)
  const setLimit = useDirectorStore((s) => s.setLimit)
  const userPresets = useWorkspaceStore((s) => s.userPresets)

  return (
    <DirectorConsolePageImpl
      scenes={scenes}
      machine={machine}
      carouselOn={carouselOn}
      carouselMs={carouselMs}
      onActivate={activate}
      onAdvance={advance}
      onCarouselChange={setCarouselOn}
      onRemoveScene={removeScene}
      onLimitChange={setLimit}
      userPresets={userPresets}
      renderCanvas={({ scene, active }) => <DirectorCanvas cards={scene.cards as never} active={active} />}
      renderAddSceneMenu={({ userPresets: presets, variant }) => (
        <DirectorAddSceneMenu userPresets={presets} variant={variant} />
      )}
    />
  )
}
