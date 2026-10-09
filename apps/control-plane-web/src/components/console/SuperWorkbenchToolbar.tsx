// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只注入路由链接渲染（受控化）。
import { Link } from 'react-router'
import { SuperWorkbenchToolbar as SuperWorkbenchToolbarView } from '@/components/views/console/WorkbenchLeafParts'
import type { WorkspacePreset } from '@/lib/console/workspace-preset'

interface SuperWorkbenchToolbarProps {
  /** 当前应用的预设 id。 */
  presetId: string
  /** 用户保存的预设（跨实例 + 单实例共享一份）。 */
  userPresets: WorkspacePreset[]
  onApplyPreset: (presetId: string) => void
  onSavePreset: (name: string) => void
  onDeletePreset: (presetId: string) => void
  /** 打开「专注终端」（沉浸终端模式，ADR-087）：取画布上首个终端卡实例。未提供=不渲染按钮。 */
  onOpenFocusTerminals?: () => void
  /** 禁用原因（画布无终端卡等）；给出时按钮禁用并以 title 提示。 */
  focusTerminalsDisabledReason?: string
}

/**
 * 超级工作台工具栏的接线层（FR-167）。
 * 视图不依赖路由库，故「进导播台」的链接经 renderLink 注入 react-router 的 Link。
 */
export default function SuperWorkbenchToolbar(props: SuperWorkbenchToolbarProps) {
  return (
    <SuperWorkbenchToolbarView
      {...props}
      renderLink={({ to, className, children }) => (
        <Link to={to} className={className}>
          {children}
        </Link>
      )}
    />
  )
}
