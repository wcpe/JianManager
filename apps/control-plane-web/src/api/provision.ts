import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import type { InstanceInfo } from '@/api/instances'

/** SpongeForge 等核心的运行时附加信息，旧响应可不返回。 */
export interface CoreRuntimeInfo {
  distribution?: string
  modFilename?: string
  forgeInstallerUrl?: string
  forgeVersion?: string
  launchJar?: string
}

/** 解析出的可下载核心构建信息（对应后端 service.CoreInfo）。 */
export interface CoreInfo {
  type: string
  mcVersion: string
  build: number
  filename: string
  downloadUrl: string
  sha256: string
  runtime?: CoreRuntimeInfo
  /** 该 MC 版本所需最低 Java 大版本（FR-316 向导 JDK 预检）；缺省=未知/不设需求，不据此拦截。 */
  javaMajorRequired?: number
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

/** 一键搭建后端子服请求体（对应后端 service.ProvisionServerRequest）。 */
export interface ProvisionServerBody {
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

/** 一键搭建异步响应（FR-319）：实例立即落库，下载/写配置在任务中心异步推进。 */
export interface ProvisionServerResult {
  instance: InstanceInfo
  /** 搭建任务 id（进度/失败原因见任务中心）。 */
  taskId: string
}

/**
 * 一键搭建后端子服（FR-319 异步化）：同步段只解析核心 + 建实例 + 登记任务（立即返回），
 * 下载核心/写配置在 CP 后台任务推进——慢源下载不再把请求拖到超时。
 */
export function useProvisionServer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ProvisionServerBody) =>
      api.post<ProvisionServerResult>('/instances/provision/server', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['tasks'] })
    },
  })
}

/** 一键搭建 Paper 子服请求体（旧 /bukkit 入口兼容）。 */
export type ProvisionBukkitBody = ProvisionServerBody

/** 旧 Paper 后端子服入口兼容，新增代码优先使用 useProvisionServer。 */
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
