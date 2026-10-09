import { SuperWorkbenchPage as SuperWorkbenchPageImpl } from '@/components/views/console/SuperWorkbenchPage'
import { useWorkspaceStore } from '@/stores/workspace'
import WorkspaceCard from './WorkspaceCard'
import TerminalPane from './TerminalPane'
import ConsoleImmersiveMode from './ConsoleImmersiveMode'
import InstanceLibrary from './InstanceLibrary'
import SuperWorkbenchToolbar from './SuperWorkbenchToolbar'

/**
 * 跨实例超级工作台的应用接线层（ADR-097）。
 *
 * 视图本体是受控视图（见 components/views）；本层注入工作台 store 的画布状态与全部动作，
 * 以及四个自带取数的子组件（卡壳 / 实例库 / 工具栏 / 专注终端沉浸台）。
 * 保留同路径的默认导出与零 props，调用点无需改动。
 */
export default function SuperWorkbenchPage() {
  const canvas = useWorkspaceStore((s) => s.superCanvas)
  const userPresets = useWorkspaceStore((s) => s.userPresets)
  const ensureSuperCanvas = useWorkspaceStore((s) => s.ensureSuperCanvas)
  const applySuperPreset = useWorkspaceStore((s) => s.applySuperPreset)
  const dropToSuper = useWorkspaceStore((s) => s.dropToSuper)
  const removeSuperCard = useWorkspaceStore((s) => s.removeSuperCard)
  const updateSuperLayout = useWorkspaceStore((s) => s.updateSuperLayout)
  const setSuperFullscreen = useWorkspaceStore((s) => s.setSuperFullscreen)
  const saveSuperPresetAs = useWorkspaceStore((s) => s.saveSuperPresetAs)
  const deleteUserPreset = useWorkspaceStore((s) => s.deleteUserPreset)

  return (
    <SuperWorkbenchPageImpl
      canvas={canvas}
      userPresets={userPresets}
      onEnsureCanvas={ensureSuperCanvas}
      onApplyPreset={applySuperPreset}
      onDropCard={dropToSuper}
      onRemoveCard={removeSuperCard}
      onLayoutChange={updateSuperLayout}
      onFullscreenChange={setSuperFullscreen}
      onSavePresetAs={saveSuperPresetAs}
      onDeletePreset={deleteUserPreset}
      renderLibrary={({ collapsed, onToggleCollapsed }) => (
        <InstanceLibrary collapsed={collapsed} onToggleCollapsed={onToggleCollapsed} />
      )}
      renderToolbar={({
        presetId,
        userPresets: presets,
        onApplyPreset: apply,
        onSavePreset: save,
        onDeletePreset: remove,
        onOpenFocusTerminals,
        focusTerminalsDisabledReason,
      }) => (
        <SuperWorkbenchToolbar
          presetId={presetId}
          userPresets={presets}
          onApplyPreset={apply}
          onSavePreset={save}
          onDeletePreset={remove}
          onOpenFocusTerminals={onOpenFocusTerminals}
          focusTerminalsDisabledReason={focusTerminalsDisabledReason}
        />
      )}
      renderCard={({ card, fullscreen, onToggleFullscreen, onClose }) => (
        <WorkspaceCard
          cardId={card.id}
          type={card.type}
          instanceId={card.instanceId ?? 0}
          fullscreen={fullscreen ?? false}
          onToggleFullscreen={onToggleFullscreen}
          onClose={onClose}
        />
      )}
      renderFocusTerminals={({ initialInstanceId, onExit }) => (
        <ConsoleImmersiveMode
          initialInstanceId={initialInstanceId}
          onExit={onExit}
          renderPane={({ instanceId: paneInstanceId, onFocus }) => (
            <TerminalPane instanceId={paneInstanceId} hideHeader embeddedImmersive onPaneFocus={onFocus} />
          )}
        />
      )}
    />
  )
}
