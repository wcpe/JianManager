import { useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import type { InstanceInfo } from '@/api/instances'

/** 一个核心 jar 候选（FR-302 导入探测，已知核心名排前）。 */
export interface ImportJarCandidate {
  /** 相对导入根、以「/」分隔的路径（深度≤2）。 */
  path: string
  size: number
  /** MANIFEST Main-Class 嗅探结果（仅排序/展示提示，可为空）。 */
  mainClassHint?: string
}

/** 一个内嵌 JDK 候选（jre / jdk / runtime / java 等子目录经 Worker 探测确认）。 */
export interface ImportJdkCandidate {
  /** JDK home 绝对路径。 */
  path: string
  vendor: string
  version: string
  majorVersion: number
  arch: string
}

/** 目录探测结果（FR-302）。 */
export interface ImportInspectResult {
  jars: ImportJarCandidate[]
  jdks: ImportJdkCandidate[]
  /** server.properties 的 server-port（0=未知）。 */
  serverPort: number
  eulaAccepted: boolean
  propsFound: boolean
}

/** 导入请求（POST /instances/import）。 */
export interface ImportServerPayload {
  nodeId: number
  path: string
  mode: 'in_place' | 'migrate'
  name: string
  jarPath: string
  jdkId?: number
  registerJdkPaths?: string[]
  memoryMb?: number
}

/** 探测节点上某现成服务器目录（平台管理员，FR-302）。 */
export function useInspectImportDir() {
  return useMutation({
    mutationFn: async (payload: { nodeId: number; path: string }) => {
      const { data } = await api.post<ImportInspectResult>('/instances/import/inspect', payload)
      return data
    },
  })
}

/** 导入现成目录为受管实例（就地接管 / 搬迁托管区，FR-302）。 */
export function useImportServer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: ImportServerPayload) => {
      const { data } = await api.post<InstanceInfo>('/instances/import', payload)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['instances'] })
      void qc.invalidateQueries({ queryKey: ['node-jdks'] })
    },
  })
}
