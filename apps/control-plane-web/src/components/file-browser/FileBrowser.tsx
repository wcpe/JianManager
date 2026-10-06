import FileBrowser from '@jianmanager/ui/components/views/file-browser/FileBrowser'
import { useThemeStore } from '@/stores/theme'

/**
 * 共享文件浏览器接线层（ADR-097）：注入解析后的主题，消费方零改动。
 */
export default function FileBrowserConnected(props: Omit<Parameters<typeof FileBrowser>[0], 'theme'>) {
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  return <FileBrowser {...props} theme={resolvedTheme === 'dark' ? 'dark' : 'light'} />
}
