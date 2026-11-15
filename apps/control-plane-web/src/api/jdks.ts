import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface NodeJDK {
  id: number
  nodeId: number
  vendor: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  createdAt: string
}

/**
 * 引用某个 JDK 的实例。
 *
 * `binding` 是**绑定方式**，不是装饰：`direct` = 实例写死了 jdk_id，删掉这个 JDK 它一定起不来；
 * `major` = 实例只声明「我要 Java 21」，启动时按大版本在节点上挑一个托管 JDK
 * （同版本还有别的托管项时不算强依赖）。删除确认文案要按它区分说法。
 */
export interface JDKRefInstance {
  id: number
  uuid: string
  name: string
  status: string
  binding: 'direct' | 'major'
}

/** `?withRefs=1` 的行：模型字段 + 引用实例 + 服务端算好的删除可行性。 */
export interface NodeJDKWithRefs extends NodeJDK {
  instances: JDKRefInstance[]
  refCount: number
  /** 服务端判定：现在删会不会 409（与 Delete 的守卫同源，前端不重算）。 */
  deleteBlocked: boolean
  /** 拒绝原因：direct / major；空串=可删。 */
  deleteBlockedReason: '' | 'direct' | 'major'
}

export function useNodeJDKs(nodeId: number) {
  return useQuery({
    queryKey: ['node-jdks', nodeId],
    queryFn: async () => {
      const { data } = await api.get<NodeJDK[]>(`/nodes/${nodeId}/jdks`)
      return data
    },
    enabled: !!nodeId,
  })
}

/**
 * 带引用关系的 JDK 清单（节点页运行时清单表用）。
 *
 * 【为什么单独一个 hook 而不是给 useNodeJDKs 加参数】两者形状不同（这里多了 instances/
 * refCount/deleteBlocked），塞进同一个 queryKey 会让「有没有引用」的缓存互相覆盖；
 * 分开后 `node-jdks` 的既有失效逻辑完全不受影响。
 *
 * 服务端把「能不能删」一起算好了——前端**不重算**绑定关系，避免出现
 * 「页面说没人用、点了却 409」这类口径漂移。
 */
export function useNodeJDKsWithRefs(nodeId: number, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-jdks-refs', nodeId],
    queryFn: async () => {
      const { data } = await api.get<NodeJDKWithRefs[]>(`/nodes/${nodeId}/jdks`, {
        params: { withRefs: 1 },
      })
      return data
    },
    enabled: !!nodeId && (options?.enabled ?? true),
  })
}

export function useCreateJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: Omit<NodeJDK, 'id' | 'nodeId' | 'createdAt'>) =>
      api.post(`/nodes/${nodeId}/jdks`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
      // 引用关系随 JDK 增删改而变（新增可能抢走大版本解析），一并失效。
      qc.invalidateQueries({ queryKey: ['node-jdks-refs', nodeId] })
    },
  })
}

export function useUpdateJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ jdkId, body }: { jdkId: number; body: Partial<Omit<NodeJDK, 'id' | 'nodeId' | 'createdAt'>> }) =>
      api.put(`/nodes/${nodeId}/jdks/${jdkId}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
      // 引用关系随 JDK 增删改而变（新增可能抢走大版本解析），一并失效。
      qc.invalidateQueries({ queryKey: ['node-jdks-refs', nodeId] })
    },
  })
}

export function useDeleteJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (jdkId: number) => api.delete(`/nodes/${nodeId}/jdks/${jdkId}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
      // 引用关系随 JDK 增删改而变（新增可能抢走大版本解析），一并失效。
      qc.invalidateQueries({ queryKey: ['node-jdks-refs', nodeId] })
    },
  })
}

/** POST /nodes/:id/jdks/install 的 202 响应：异步任务受理，回执 taskId。 */
export interface InstallJDKAccepted {
  taskId: string
}

/** 
 * 一键下载安装 JDK（异步化）。
 * 由同步阻塞（最长 20min 返回 JDK 记录）改为**异步**：发起即返回 taskId（HTTP 202），
 * 进度/完成经全局任务中心（`/tasks`）与站内信查看；JDK 列表在任务完成后由心跳落库、随后失效刷新。
 * 这里仍失效 node-jdks 缓存（受理后立即刷一次；真正出现新条目要等任务完成，前端可由任务中心引导）。
 */
/** JDK 探测结果（对齐后端 ProbeResult）。 */
export interface ProbeResult {
  valid: boolean
  vendor: string
  majorVersion: number
  version: string
  arch: string
  javaHome: string
  error?: string
}

/** 
 * 探测节点上某路径（JDK home 或 java 可执行文件）的 JDK。
 * 登记前选目录后调用，后端跑 `java -version` 自动得出厂商/版本/架构，免手填。
 */
export function useProbeJDK(nodeId: number) {
  return useMutation({
    mutationFn: (path: string) =>
      api.post<ProbeResult>(`/nodes/${nodeId}/jdks/probe`, { path }).then((r) => r.data),
  })
}

export function useInstallJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    // version 可选：非空时经 foojay 按具体版本解析；为空取该大版本最新。
    mutationFn: (body: { vendor: string; majorVersion: number; arch: string; version?: string }) =>
      api.post<InstallJDKAccepted>(`/nodes/${nodeId}/jdks/install`, body).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
      qc.invalidateQueries({ queryKey: ['tasks'] })
    },
  })
}
