import CodeEditor from '@jianmanager/ui/components/views/explorer/CodeEditor'
import { useThemeStore } from '@/stores/theme'

/**
 * CodeMirror 编辑器接线层（ADR-097）：把主题 store 的解析结果注入包内视图，
 * 消费方（ResourceExplorer / DecompileViewer 等）零改动。
 */
export default function CodeEditorConnected(props: Omit<Parameters<typeof CodeEditor>[0], 'theme'>) {
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  return <CodeEditor {...props} theme={resolvedTheme === 'dark' ? 'dark' : 'light'} />
}
