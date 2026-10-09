import { toast } from 'sonner'
import { ConsoleOutputView as ConsoleOutputViewImpl } from '@/components/views/console/ConsoleOutputView'
import type { ConsoleOutputViewProps } from '@jianmanager/ui'

// 类型转出：调用点（InstanceConsoleView 等）沿用原路径导入，零改动。
export type { ConsoleOutputHandle, ConsoleHistoryBanner, ConsoleOutputViewProps } from '@jianmanager/ui'

/**
 * 控制台输出区的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控（不弹 toast）；本层补上「提示通道」——把复制回执
 * 接回应用侧的 toast，并原样透传其余 props 与 ref，调用点无需改动。
 */
export default function ConsoleOutputView(props: ConsoleOutputViewProps) {
  return (
    <ConsoleOutputViewImpl
      {...props}
      onNotify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
