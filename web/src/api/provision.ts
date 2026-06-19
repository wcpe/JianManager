import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import type { InstanceInfo } from '@/api/instances'

/** 解析出的可下载核心构建信息（对应后端 service.CoreInfo）。 */
export interface CoreInfo {
  type: string
  mcVersion: string
  build: number
  filename: string
  downloadUrl: string
  sha256: string
}

interface CoreVersionsResp {
  type: string
  versions: string[]
}

/** 列出指定核心类型的可用 MC 版本（后端已反转为新→旧）。 */
export function useCoreVersions(coreType: string) {
  return useQuery({
    queryKey: ['core-versions', coreType],
    queryFn: async () => {
      const { data } = await api.get<CoreVersionsResp>('/cores', { params: { type: coreType } })
      return data.versions
    },
    enabled: !!coreType,
    staleTime: 5 * 60 * 1000,
  })
}

/**
 * 解析指定核心类型/版本的下载信息（build<=0 取最新构建），用于提交前向用户预览
 * 「将下载哪个构建 + 校验值」，不触发实际下载。
 */
export function useResolvedCore(coreType: string, mcVersion: string, build: number) {
  return useQuery({
    queryKey: ['core-resolve', coreType, mcVersion, build],
    queryFn: async () => {
      const { data } = await api.get<CoreInfo>('/cores', {
        params: { type: coreType, mcVersion, build: build > 0 ? build : undefined },
      })
      return data
    },
    enabled: !!coreType && !!mcVersion,
  })
}

/** 一键搭建 Paper 子服请求体（对应后端 service.ProvisionBukkitRequest）。 */
export interface ProvisionBukkitBody {
  nodeId: number
  name: string
  coreType: string
  mcVersion: string
  build?: number
  jdkId?: number
  memoryMb?: number
  jvmArgs?: string[]
  groupId?: number
  /** 是否向 Mojang 校验正版（缺省 false=代理就绪/离线）。 */
  onlineMode?: boolean
}

/**
 * 一键搭建 Paper 后端子服：后端解析核心 → 分配端口/工作目录 → 结构化启动 →
 * 下载核心 + 写基础配置，返回创建的实例（STOPPED，可一键启动）。
 */
export function useProvisionBukkit() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ProvisionBukkitBody) =>
      api.post<InstanceInfo>('/instances/provision/bukkit', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
}
