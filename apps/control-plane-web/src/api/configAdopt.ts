import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { apiErrorMessage } from '@/lib/shared/api-error'

/**
 * 配置纳管（FR-533）：把服务器磁盘上**已经存在**的配置文件读入为 `config_file` + 实例级 v1 版本。
 *
 * 【这不是数据导入】配置的真源就是磁盘。本模块只「记录现状」：
 * - 内容与磁盘**逐字节一致**（后端直取 Worker 读到的原文，不重新序列化、不排序键）；
 * - `contentHash` 就是磁盘文件的 sha256；
 * - **不改写任何文件**（纳管前后 mtime 不变）；
 * - 已纳管（该实例作用域已有版本）返回 `ALREADY_ADOPTED`，**绝不覆盖**——
 *   否则会冲掉运维后续做的修改；
 * - 坏掉的配置也照收（记录现状优先于校验通过），解析失败只作为 `warning` 带出。
 *
 * 契约见 `docs/API.md` 的「分层配置与纳管（FR-529~533）」与
 * `docs/specs/config-beacon-migration/spec.md`。
 */

/**
 * 纳管动作：让界面能说清「将要 / 已经做了什么」。
 *
 * 与后端 `service.AdoptAction*` 常量逐值对应。
 */
export type AdoptAction =
  | 'none'
  | 'create_file_and_baseline'
  | 'append_baseline'
  | 'create_file_only'

/** 逐文件纳管状态（后端 `service.AdoptStatus*`）。 */
export type AdoptFileStatus =
  | 'created'
  | 'appended'
  | 'unchanged'
  | 'already_adopted'
  | 'failed'

/**
 * 纳管错误码（spec §6 + §10.1 补全）。
 *
 * 前端按码分类展示，不靠 message 文本猜——文案会变，码不会。
 */
export type AdoptCode =
  | 'ADOPTED'
  | 'ALREADY_ADOPTED'
  | 'FILE_NOT_FOUND'
  | 'READ_FAILED'
  | 'INSTANCE_OFFLINE'
  | 'PATH_ESCAPE'
  | 'INSTANCE_NOT_FOUND'
  | 'FORMAT_UNSUPPORTED'
  | 'FORMAT_CONFLICT'
  | 'CONFIG_FILE_ARCHIVED'
  | 'WRITE_FAILED'

/** 单个文件的纳管结果（后端 `service.AdoptFileResult`）。 */
export interface AdoptFileResult {
  instanceId: number
  instanceName?: string
  /** 规范化后的相对路径。 */
  path: string
  /** `config_file` 逻辑名。 */
  name: string
  format?: string
  /** 内容字节数（== 磁盘文件大小）。 */
  size: number
  /** sha256(磁盘原文) 小写 hex，与写入层版本的 content_hash 一致。 */
  contentHash?: string
  configFileId?: number
  versionId?: number
  versionNo?: number
  status: AdoptFileStatus
  action?: AdoptAction
  code?: AdoptCode
  message?: string
  /**
   * 内容可读但解析告警（如 YAML 语法错误）。
   * 纳管照常记录现状，因此这**不是失败**——界面须与 `status=failed` 区别展示。
   */
  warning?: string
  dryRun: boolean
}

/** 逐实例结果（批量纳管形态）。 */
export interface AdoptInstanceResult {
  instanceId: number
  instanceName?: string
  /** `matched` / `no_match` / `failed`。 */
  status: 'matched' | 'no_match' | 'failed'
  code?: string
  message?: string
  /** Discover 到的配置文件总数。 */
  discoveredFiles: number
  /** 命中 pathPatterns 的文件数。 */
  matchedFiles: number
  /** Discover 被规模上限截断（结果可能不完整）。 */
  truncated: boolean
  results?: AdoptFileResult[]
}

/** 纳管汇总。 */
export interface AdoptSummary {
  total: number
  created: number
  /** 复用了已有 config_file 并追加基线版本。 */
  appended: number
  unchanged: number
  alreadyAdopted: number
  failed: number
  /** 批量时参与的目标实例数。 */
  instances?: number
  /** Discover 被截断的实例数。 */
  truncatedInstances?: number
  dryRun: boolean
}

/** 纳管响应。 */
export interface AdoptResponse {
  /** 单文件/多文件纳管的逐文件结果（批量形态为空，逐实例结果在 instances 里）。 */
  results: AdoptFileResult[]
  instances?: AdoptInstanceResult[]
  summary: AdoptSummary
  /** 提醒调用方本次没有写库。 */
  dryRun: boolean
}

/** 待纳管的单个文件。 */
export interface AdoptFileInput {
  instanceId: number
  /** 实例工作目录内的相对路径。 */
  path: string
  /** 可选的 config_file 逻辑名；留空则用 path。 */
  asName?: string
}

/** 批量纳管目标（复用业务目标解析器：分组子树 or 显式实例列表）。 */
export interface AdoptTarget {
  groupIds?: number[]
  includeDescendants?: boolean
  instanceIds?: number[]
}

/**
 * 纳管请求。两种形态共用一个入口：
 * - 给 `target` → 批量（`target` + `pathPatterns`）；
 * - 否则 → 按 `files` 列表逐文件纳管。
 */
export interface AdoptRequest {
  files?: AdoptFileInput[]
  target?: AdoptTarget
  /**
   * 批量模式的通配过滤。**必填**（后端刻意不允许「纳管目录下全部文件」）。
   * 语法：逐段 glob，`**` 跨层，大小写不敏感。
   */
  pathPatterns?: string[]
  /** 是否创建实例级 v1 版本。 */
  createBaseline: boolean
  /** 只报告不写库（预演端点会强制为 true，无需前端自己翻）。 */
  dryRun?: boolean
}

/** JM 侧配置规模自检视图（后端 `service.ConfigHealthView`）。 */
export interface ConfigHealthView {
  configFileCount: number
  layerVersionCount: number
  /** 最近一次配置写入时间（RFC3339）；从未写入时缺省。 */
  lastWriteAt?: string
  /**
   * 固定提示文案。
   *
   * **只报告 JM 侧事实，不做自动判定**：JM 无法从自身探测 Beacon 侧的配置开关
   * （那是另一个系统的状态），据此告警会把正常的拓扑同步误报成双写风险。
   * 因此这里刻意保留后端下发的原话，不在前端改写。
   */
  notice: string
}

/** 取配置规模自检（GET /config-files/health）。 */
export function useConfigHealth(enabled = true) {
  return useQuery({
    queryKey: ['config-files', 'health'],
    queryFn: async () => {
      const { data } = await api.get<ConfigHealthView>('/config-files/health')
      return data
    },
    enabled,
  })
}

/**
 * 纳管预演（POST /config-files/adopt/preview）。
 *
 * 独立端点而非 `dryRun: true`：后端**强制**按 dryRun 执行、无视请求体里的该字段——
 * 「预演端点真的没写库」是调用方唯一合理的期待，若允许被请求体覆盖就不能当安全预检用。
 */
export function useAdoptPreview() {
  return useMutation({
    mutationFn: async (body: AdoptRequest) => {
      const { data } = await api.post<AdoptResponse>('/config-files/adopt/preview', body)
      return data
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '纳管预演失败'))
    },
  })
}

/**
 * 执行纳管（POST /config-files/adopt）。
 *
 * 不做乐观更新：纳管的结论是逐文件的（created / already_adopted / failed / warning），
 * 先刷成「已纳管」再回退会让运维以为基线已经存在。
 */
export function useAdoptConfigs() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: AdoptRequest) => {
      const { data } = await api.post<AdoptResponse>('/config-files/adopt', body)
      return data
    },
    onSuccess: (res) => {
      // 纳管会新建 config_file 与实例层 v1：列表、层版本与有效值预览全部受影响。
      void qc.invalidateQueries({ queryKey: ['config-files'] })
      void qc.invalidateQueries({ queryKey: ['configs'] })
      const { created, appended, alreadyAdopted, failed } = res.summary
      toast.success(
        `纳管完成：新建 ${created}，追加 ${appended}，已纳管 ${alreadyAdopted}，失败 ${failed}`,
      )
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '纳管失败'))
    },
  })
}
