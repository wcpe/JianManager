import FilePreview from '@/components/views/file-browser/FilePreview'
import { useThemeStore } from '@/stores/theme'

/**
 * 文件预览接线层（ADR-097）：注入解析后的主题，消费方零改动。
 */
export default function FilePreviewConnected(props: Omit<Parameters<typeof FilePreview>[0], 'theme'>) {
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  return <FilePreview {...props} theme={resolvedTheme === 'dark' ? 'dark' : 'light'} />
}
