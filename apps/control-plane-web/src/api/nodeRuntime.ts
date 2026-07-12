import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 一条节点制品缓存项（FR-178）。name/version 由 CP 用 asset 表按 sha256 补全（可能为空）。 */
export interface ArtifactCacheItem {
  sha256: string
  name: string
  type: string
  version: string
  size: number
  /** 首次存入时间（Unix 秒）。 */
  cachedAt: number
  /** 最近命中/存入时间（Unix 秒，LRU 依据）。 */
  lastUsedAt: number
}

/** 节点制品缓存视图：列表 + 总占用 + 当前上限（FR-178）。 */
export interface ArtifactCacheView {
  items: ArtifactCacheItem[]
  totalBytes: number
  /** 容量上限（字节，0=不限）。 */
  capBytes: number
}

/** 一条可选 JDK 构建（foojay 版本选择器，FR-178）。 */
export interface JDKCatalogPackage {
  distribution: string
  majorVersion: number
  javaVersion: string
  archiveType: string
  latest: boolean
}

/** 目录浏览中的一个子目录项（FR-178 目录选择器）。 */
export interface BrowseDirEntry {
  name: string
  path: string
}

/** 目录浏览结果（FR-178）。 */
export interface BrowseDirView {
  /** 当前浏览的规范化绝对路径（空=起点列表，如盘符/根）。 */
  path: string
  /** 父目录绝对路径（已在根则为空）。 */
  parent: string
  dirs: BrowseDirEntry[]
}

/** 查询节点制品缓存（列表 + 总占用 + 上限，仅平台管理员）。 */
export function useArtifactCache(nodeId: number, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['artifact-cache', nodeId],
    queryFn: async () => {
      const { data } = await api.get<ArtifactCacheView>(`/nodes/${nodeId}/artifact-cache`)
      return data
    },
    enabled: (options?.enabled ?? true) && !!nodeId,
  })
}

/** 逐项清除某 sha256 的缓存（FR-178）。 */
export function useEvictArtifactCache(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (sha256: string) => api.delete(`/nodes/${nodeId}/artifact-cache/${sha256}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-cache', nodeId] }),
  })
}

/** 清空节点全部制品缓存（FR-178）。 */
export function useClearArtifactCache(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.post(`/nodes/${nodeId}/artifact-cache/clear`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-cache', nodeId] }),
  })
}

/** 设置缓存容量上限（字节，0=不限；超限按 LRU 淘汰，FR-178）。 */
export function useSetArtifactCacheCap(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (capBytes: number) =>
      api.put<ArtifactCacheView>(`/nodes/${nodeId}/artifact-cache/cap`, { capBytes }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-cache', nodeId] }),
  })
}

/**
 * 查询某发行版可选的具体 JDK 版本（经 CP 代理 foojay，FR-178）。
 * 仅在 vendor 非空且面板打开时启用；失败（foojay 不可达）由调用方降级为「手填版本」。
 */
export function useJDKCatalog(nodeId: number, vendor: string, major: number, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['jdk-catalog', nodeId, vendor, major],
    queryFn: async () => {
      const params = new URLSearchParams({ vendor })
      if (major > 0) params.set('major', String(major))
      const { data } = await api.get<JDKCatalogPackage[]>(`/nodes/${nodeId}/jdk/catalog?${params.toString()}`)
      return data
    },
    enabled: (options?.enabled ?? true) && !!nodeId && vendor.trim() !== '',
    retry: false,
    staleTime: 5 * 60 * 1000,
  })
}

/**
 * 浏览节点某绝对路径下的子目录（经 CP 委托 Worker，FR-178 目录选择器）。
 * path 为空时返回起点列表（盘符/根）。仅在面板打开时启用。
 */
export function useBrowseDir(nodeId: number, path: string, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['browse-dir', nodeId, path],
    queryFn: async () => {
      const params = new URLSearchParams()
      if (path) params.set('path', path)
      const { data } = await api.get<BrowseDirView>(`/nodes/${nodeId}/browse?${params.toString()}`)
      return data
    },
    enabled: (options?.enabled ?? true) && !!nodeId,
    retry: false,
  })
}
