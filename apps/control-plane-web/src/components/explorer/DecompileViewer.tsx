import DecompileViewer from '@/components/views/explorer/DecompileViewer'
import { decompile } from '@/api/archive'
import { useThemeStore } from '@/stores/theme'

/**
 * 反编译视图接线层（ADR-097）：注入反编译取数与解析后的主题，消费方零改动。
 */
export default function DecompileViewerConnected(
  props: Omit<Parameters<typeof DecompileViewer>[0], 'decompile' | 'theme'>,
) {
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  return <DecompileViewer {...props} decompile={decompile} theme={resolvedTheme === 'dark' ? 'dark' : 'light'} />
}
