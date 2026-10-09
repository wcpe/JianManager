import { useCallback, useState } from 'react'
import { toast } from 'sonner'
import { ConsoleImmersiveMode as ConsoleImmersiveModeImpl } from '@/components/views/console/ConsoleImmersiveMode'
import type { ConsoleImmersiveModeProps as ConsoleImmersiveModePropsFull } from '@jianmanager/ui'
import { useInstance, useInstanceSearch } from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'

/**
 * 对外 props（沿用原导出名）：接线层自己注入的字段从调用点口径里排除，
 * 调用点（TerminalPane / 页面）只传原来那套 props，零改动。
 */
export type ConsoleImmersiveModeProps = Omit<
  ConsoleImmersiveModePropsFull,
  'instances' | 'isSearchingInstances' | 'onSearchInstances' | 'usePaneData' | 'onNotify'
>

/**
 * 按实例取详情与指标（模块级定义，引用稳定）。
 *
 * pane 是运行期动态增删的，外壳无法预知要为哪些实例取数，故把「取数」本身
 * 以 hook 形式注入视图；TanStack 缓存让同一实例的重复查询天然共享。
 */
function usePaneData(instanceId: number) {
  const { data: instance } = useInstance(instanceId)
  const { data: metrics } = useInstanceMetrics(instanceId)
  return { instance, metrics }
}

/**
 * 沉浸控制台工作台的应用接线层（ADR-097）。
 *
 * 视图本体是受控视图（见 components/views）；本层注入实例选择器的取数（仅选择器打开时查询）、
 * 按 pane 的实例/指标查表器，并把提示回执接回 toast。其余 props 原样透传。
 */
export default function ConsoleImmersiveMode({ initialInstanceId, onExit, renderPane }: ConsoleImmersiveModeProps) {
  const [search, setSearch] = useState({ open: false, query: '' })
  const { data: instanceSearch, isFetching } = useInstanceSearch(
    { q: search.query || undefined, page: 1, pageSize: 20 },
    search.open,
  )
  const onSearchInstances = useCallback((params: { open: boolean; query: string }) => setSearch(params), [])

  return (
    <ConsoleImmersiveModeImpl
      initialInstanceId={initialInstanceId}
      onExit={onExit}
      renderPane={renderPane}
      instances={instanceSearch?.items}
      isSearchingInstances={isFetching}
      onSearchInstances={onSearchInstances}
      usePaneData={usePaneData}
      onNotify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
