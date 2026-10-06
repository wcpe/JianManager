import { DirectorSceneStrip as DirectorSceneStripView } from '@jianmanager/ui'
import { useDirectorStore } from '@/stores/director'

/**
 * 导播台缩略图条的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层把导播 store 的场景列表、状态机快照与三个动作注入进去，
 * 保留同路径的默认导出与零 props，调用点无需改动。
 */
export default function DirectorSceneStrip() {
  const scenes = useDirectorStore((s) => s.scenes)
  const machine = useDirectorStore((s) => s.machine)
  const activate = useDirectorStore((s) => s.activate)
  const removeScene = useDirectorStore((s) => s.removeScene)
  const setLimit = useDirectorStore((s) => s.setLimit)

  return (
    <DirectorSceneStripView
      scenes={scenes}
      machine={machine}
      onActivate={activate}
      onRemove={removeScene}
      onLimitChange={setLimit}
    />
  )
}
