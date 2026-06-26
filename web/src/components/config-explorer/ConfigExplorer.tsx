import { useCallback, useEffect, useRef, useState } from 'react'
import ResourceExplorer, { type ConfigCapabilities } from '@/components/explorer/ResourceExplorer'
import ConfigFileEditor from './ConfigFileEditor'
import ConfigVersionDrawer from './ConfigVersionDrawer'
import FavoritesBar from './FavoritesBar'
import {
  browserStorage,
  loadFavorites,
  saveFavorites,
  toggleFavorite as toggleFav,
} from './favorites'

/**
 * 配置管理资源管理器（FR-071）。
 *
 * **复用 FR-070 `ResourceExplorer`**（左树右内容/编辑器 + 交互全集：重命名/多选/批量/拖拽/
 * 剪切粘贴/移动/新建/删除/上传/下载），并经 `config` 能力注入叠加配置语义：
 * - 打开文件 → 配置编辑器（schema 表单/文本双模式 + 跨文件校验 + Ctrl+S 存配置版本，FR-031）；
 * - 左栏顶部 → 收藏（书签，localStorage）+ 已发现配置（`GET /configs/discover` 递归全部配置）；
 * - 历史 → 配置版本抽屉（FR-031 版本/diff/回滚）。
 *
 * 树/列表本身呈现工作目录全部文件（含非配置），满足「目录树呈现自动发现的全部配置」。
 */
interface ConfigExplorerProps {
  instanceId: number
}

export default function ConfigExplorer({ instanceId }: ConfigExplorerProps) {
  const storage = browserStorage()
  const [favorites, setFavorites] = useState<string[]>(() => loadFavorites(storage, instanceId))

  // 切换实例时重载该实例的收藏。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 实例切换时从存储重载收藏，属合法同步
    setFavorites(loadFavorites(storage, instanceId))
    // eslint-disable-next-line react-hooks/exhaustive-deps -- storage 单例，仅随 instanceId 重载
  }, [instanceId])

  const toggleFavorite = useCallback(
    (path: string) => {
      setFavorites((prev) => {
        const next = toggleFav(prev, path)
        saveFavorites(storage, instanceId, next)
        return next
      })
    },
    [storage, instanceId],
  )

  // 资源管理器暴露的「按路径打开」句柄，供收藏/发现面板点选。
  const openRef = useRef<((path: string) => void) | null>(null)
  const handleOpen = useCallback((path: string) => {
    openRef.current?.(path)
  }, [])

  const config: ConfigCapabilities = {
    renderEditor: ({ instanceId: iid, path, name, onClose, onAfterSave, onOpenVersions, onDirtyChange }) => (
      <ConfigFileEditor
        instanceId={iid}
        path={path}
        name={name}
        onClose={onClose}
        onAfterSave={onAfterSave}
        onOpenVersions={onOpenVersions}
        onDirtyChange={onDirtyChange}
      />
    ),
    renderVersionDrawer: ({ instanceId: iid, filePath, open, onOpenChange, onRolledBack }) => (
      <ConfigVersionDrawer
        instanceId={iid}
        filePath={filePath}
        open={open}
        onOpenChange={onOpenChange}
        onRolledBack={onRolledBack}
      />
    ),
    sidebarExtra: (
      <FavoritesBar
        instanceId={instanceId}
        favorites={favorites}
        onToggleFavorite={toggleFavorite}
        onOpen={handleOpen}
      />
    ),
  }

  return (
    <ResourceExplorer
      instanceId={instanceId}
      config={config}
      openPathRef={(open) => {
        openRef.current = open
      }}
    />
  )
}
