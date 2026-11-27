import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 统一 Runtime 视图行：node_jdks(type=jdk) + node_runtimes 读侧拼装。 */
export interface NodeRuntimeItem {
  id: number
  nodeId: number
  /** 运行时类型：jdk / nodejs / python（预留）。 */
  type: string
  /** 展示名：jdk=厂商（Temurin/...）；其它=登记名（如 "Node.js 22"）。 */
  name: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  createdAt: string
}

/** 一条扫描发现的运行时候选。 */
export interface RuntimeCandidate {
  type: string
  vendor: string
  version: string
  majorVersion: number
  arch: string
  path: string
  /** 已在库（托管根下 / DB 已登记同路径）：默认不勾选、不可重复入库。 */
  alreadyRegistered: boolean
}

/** 登记请求体（POST /nodes/:id/runtimes）：type=jdk 转发现有 JDK 链路，其它落 node_runtimes。 */
export interface RegisterRuntimeBody {
  type: string
  name?: string
  vendor?: string
  majorVersion: number
  version: string
  arch?: string
  path: string
  managed?: boolean
}

/** GET /nodes/:id/runtimes — 统一 Runtime 视图（含 syncFromWorker 容忍语义）。 */
export function useNodeRuntimes(nodeId: number, opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-runtimes', nodeId],
    queryFn: async () => {
      const { data } = await api.get<NodeRuntimeItem[]>(`/nodes/${nodeId}/runtimes`)
      return data
    },
    enabled: !!nodeId && (opts?.enabled ?? true),
  })
}

/** POST /nodes/:id/runtimes/scan — 扫描节点常见安装路径回候选列表（节点离线 503）。 */
export function useScanRuntimes(nodeId: number) {
  return useMutation({
    mutationFn: (types?: string[]) =>
      api
        .post<{ candidates: RuntimeCandidate[] }>(`/nodes/${nodeId}/runtimes/scan`, { types: types ?? [] })
        .then((r) => r.data.candidates),
  })
}

/**
 * 探测结果（POST /nodes/:id/runtimes/probe）。
 *
 * `valid=false` 是**常规业务结果**而不是请求失败：用户输错路径、或那个路径确实不是
 * 该运行时，都返回 200 + valid=false + error 文案。界面直接把 error 显示在输入框下方，
 * 不该当成"服务出错"弹错误 toast。
 */
export interface RuntimeProbeResult {
  valid: boolean
  /** 失败原因（valid=false 时有值）。 */
  error?: string
  /** 探测成功时的候选（字段同扫描候选，alreadyRegistered 由 CP 按 DB 补标）。 */
  candidate?: RuntimeCandidate
}

/**
 * POST /nodes/:id/runtimes/probe — 探测**指定路径**上的运行时。
 *
 * 【为什么需要它】扫描（`useScanRuntimes`）只在节点上硬编码的常见目录里找。
 * 用户把运行时装在 /opt/my-runtime 这类位置时扫不到，但他自己知道路径。
 * 登记接口又要求提供 version/majorVersion——让用户手填既容易错也没必要，
 * 节点上跑一次 `node --version` 就是准确值。
 */
export function useProbeRuntime(nodeId: number) {
  return useMutation({
    mutationFn: (body: { type: string; path: string }) =>
      api.post<RuntimeProbeResult>(`/nodes/${nodeId}/runtimes/probe`, body, { timeout: 25_000 }).then((r) => r.data),
  })
}

/** POST /nodes/:id/runtimes — 登记单条运行时（扫描候选入库 / 手动登记泛化）。 */
export function useRegisterRuntime(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: RegisterRuntimeBody) =>
      api.post<NodeRuntimeItem>(`/nodes/${nodeId}/runtimes`, body).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-runtimes', nodeId] })
      // type=jdk 落 node_jdks：JDK 列表与带引用视图一并失效。
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
      qc.invalidateQueries({ queryKey: ['node-jdks-refs', nodeId] })
    },
  })
}

/** 安装请求体（POST /nodes/:id/runtimes/install）：首批仅 type=nodejs。 */
export interface InstallRuntimeBody {
  type: string
  major: number
  /** 可空：Worker 按节点本机推导（nodejs 命名 x64/arm64）。 */
  arch?: string
}

/** 安装回执：202 + 任务中心任务（kind=runtime_install）。 */
export interface InstallRuntimeResult {
  taskId: string
  /** true = 同参数的安装已在跑，本次是复用（200 而非 202）。 */
  reused?: boolean
}

/** POST /nodes/:id/runtimes/install — 异步一键安装 Node.js（202+taskId，进度在任务中心）。 */
export function useInstallRuntime(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: InstallRuntimeBody) =>
      api.post<InstallRuntimeResult>(`/nodes/${nodeId}/runtimes/install`, body).then((r) => r.data),
    onSuccess: () => {
      // 终态落库由心跳完成，安装期间列表不变；失效使任务完成后的回看拿到新行。
      qc.invalidateQueries({ queryKey: ['node-runtimes', nodeId] })
    },
  })
}

/** DELETE /nodes/:id/runtimes/:rid?type= — 删除（type 定位承载表；jdk/托管 nodejs 连文件、外部只删记录）。 */
export function useDeleteRuntime(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, type }: { id: number; type: string }) =>
      api.delete(`/nodes/${nodeId}/runtimes/${id}?type=${encodeURIComponent(type)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-runtimes', nodeId] })
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
      qc.invalidateQueries({ queryKey: ['node-jdks-refs', nodeId] })
    },
  })
}
