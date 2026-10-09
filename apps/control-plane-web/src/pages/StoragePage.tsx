import { toast } from 'sonner'
import { useQueryClient } from '@tanstack/react-query'

import StoragePage from '@/components/views/StoragePage'
import { useStorageOverview, clearStorageCache } from '@/api/storage'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import ResourceExplorer from '@/components/explorer/ResourceExplorer'
import { storageFileSource } from '@/components/file-browser/sources/storageSource'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/**
 * 平台存储页接线层（ADR-097）：概览取数、cache 清理、权限判定、数据源与浏览壳依赖都在这里注入。
 */
export default function StoragePageConnected(
  props: Omit<
    Parameters<typeof StoragePage>[0],
    'isPlatformAdmin' | 'overview' | 'isLoading' | 'isError' | 'onClearCache' | 'storageSource' | 'shellDeps' | 'notify'
  >,
) {
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.role)
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  const { data, isLoading, isError } = useStorageOverview()

  return (
    <StoragePage
      {...props}
      isPlatformAdmin={role === ROLE_PLATFORM_ADMIN}
      overview={data}
      isLoading={isLoading}
      isError={isError}
      onClearCache={clearStorageCache}
      storageSource={storageFileSource()}
      shellDeps={{
        theme: resolvedTheme === 'dark' ? 'dark' : 'light',
        // browser 模式下标签宿主不被渲染，这里只为满足类型。
        tabHost: {
          notify: (kind, message) => (kind === 'error' ? toast.error(message) : toast.success(message)),
          renderExplorer: (args) => <ResourceExplorer {...args} />,
        },
      }}
      onCacheCleared={() => {
        // 清理后占用变化：失效概览与文件列表缓存（原实现在按钮内 invalidate）。
        qc.invalidateQueries({ queryKey: ['storage', 'overview'] })
        qc.invalidateQueries({ queryKey: ['storage', 'files'] })
      }}
      notify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
