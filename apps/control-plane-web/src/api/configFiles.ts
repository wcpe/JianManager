import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 
 * 分层配置对内 API：配置文件元数据 + 各作用域的不可变层版本链 + 有效值预览。
 *
 * 契约来源：
 * - 后端路由 `internal/controlplane/router/config_file.go`（12 个端点）；
 * - 服务层视图 `internal/controlplane/service/config_layer.go` / `config_merge.go`；
 * - 文档 `docs/API.md`「分层配置与纳管（~533）」与 `docs/specs/config-layered-override/spec.md`。
 *
 * 与既有 `/instances/:id/configs/*`（直接读写某台实例的某个文件）刻意分开：
 * 这里管的是「一条配置在多个作用域上的分层定义」。
 */

/** 配置格式（创建后不可改，决定解析与合并规则）。 */
export type ConfigFileFormat = 'yaml' | 'json' | 'properties'

/** 层作用域级别：分组级（任意分组树层级）/ 实例级（优先级最高）。 */
export type ConfigScopeLevel = 'group' | 'instance'

/** 可选的配置格式（与后端 `model.ValidConfigFileFormat` 对齐）。 */
export const CONFIG_FILE_FORMATS: readonly ConfigFileFormat[] = ['yaml', 'json', 'properties']

/** 
 * 配置文件视图（`service.ConfigFileView`）：`model.ConfigFile` 全字段 + `layerCount`。
 *
 * `sensitivePaths` 后端是 JSON 数组**文本**（覆盖保存时才编码），故用 {@link parseSensitivePaths}
 * 规范化后再展示；`createdAt` / `updatedAt` 为 Go `time.Time` 的 RFC3339 字符串。
 */
export interface ConfigFileView {
  id: number
  name: string
  format: ConfigFileFormat
  description: string
  /** JSON Schema 文本（可空）。 */
  schemaJson?: string
  /** 敏感键路径的 JSON 数组文本（可空）。 */
  sensitivePaths?: string
  createdBy: number
  createdAt: string
  updatedAt: string
  /** 已有层（作用域）数量。 */
  layerCount: number
}

/** 某作用域的当前 head 版本摘要（不含 content，避免列表过大）。 */
export interface ConfigLayerHead {
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  /** 分组名 / 实例名，便于直接展示。 */
  scopeName: string
  versionId: number
  versionNo: number
  contentHash: string
  remark: string
  createdBy: number
  /** Unix 秒（后端 `CreatedAt.Unix()`）。 */
  createdAt: number
  /** 原文字节数。 */
  contentSize: number
}

/** 层版本历史条目（`service.LayerVersionView`，含原文）。 */
export interface ConfigLayerVersionView {
  id: number
  versionNo: number
  contentHash: string
  content: string
  remark: string
  createdBy: number
  /** Unix 秒。 */
  createdAt: number
  /** 回退产生的版本指回来源版本；普通保存为空。 */
  basedOnVersionId?: number
}

/** 保存层版本后返回的版本行（`model.ConfigLayerVersion`）。 */
export interface ConfigLayerVersion {
  id: number
  configFileId: number
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  versionNo: number
  content: string
  contentHash: string
  basedOnVersionId?: number
  remark: string
  createdBy: number
  /** RFC3339 字符串。 */
  createdAt: string
}

/** 参与合并的某一层（`service.LayerSource`）。 */
export interface LayerSource {
  /** `group` / `instance`。 */
  level: string
  /** 分组 ID 或实例 ID。 */
  id: number
  /** 分组名 / 实例名。 */
  name?: string
  versionNo: number
}

/** 单个键的溯源条目（`service.TracedValue`）。 */
export interface TracedEntry {
  value: unknown
  source: LayerSource
  /** true 表示该键被 `null` 删除（yaml/json 语义）。 */
  deleted?: boolean
}

/** 合并告警（`service.MergeWarning`）。 */
export interface MergeWarning {
  /** `error` 表示该层整体失效；`warning` 仅提示。 */
  level: string
  /** 稳定标识：LAYER_PARSE_FAILED / TYPE_CONFLICT / ROOT_NOT_OBJECT。 */
  code: string
  key?: string
  message: string
  source: LayerSource
}

/** 合并结果未通过 schema 的问题（`service.SchemaIssue`）。 */
export interface SchemaIssue {
  level: string
  path?: string
  message: string
}

/** 有效值预览（`service.EffectivePreview`）。 */
export interface EffectiveResult {
  instanceId: number
  configFileId: number
  format: string
  /** 
   * 合并后的完整文本。
   *
   * ⚠️ 这是**规范化结果**：键按字典序重排、注释全部丢失、缩进为解析器输出格式。
   * 不可直接当磁盘原文使用（见 spec §7.4 与页面上的明示提示）。
   */
  content: string
  effectiveHash: string
  /** 每个键 → 来源层与版本号；被删除的键带 `deleted: true`。 */
  traced: Record<string, TracedEntry>
  warnings: MergeWarning[]
  schemaIssues: SchemaIssue[]
  /** 实际参与合并的作用域链（从低到高优先级）。 */
  layers: LayerSource[]
}

/** 创建配置文件的入参。 */
export interface CreateConfigFileInput {
  name: string
  format: ConfigFileFormat
  description?: string
  schemaJson?: string
  sensitivePaths?: string[]
}

/** 修改元数据的入参（name / format 创建后不可改，传了会 400）。 */
export interface UpdateConfigFileInput {
  description?: string
  schemaJson?: string
  /** 传空数组表示清空（后端用指针区分「不传」与「清空」）。 */
  sensitivePaths?: string[]
}

/** 保存新层版本的入参。 */
export interface SaveLayerInput {
  scopeType: ConfigScopeLevel
  scopeId: number
  content: string
  remark?: string
}

/** 后端错误信封（网关约定 `{error, message}`；校验失败额外带 `issues`）。 */
export interface ConfigFilesErrorBody {
  /** 稳定错误码：INVALID_REQUEST / NOT_FOUND / ALREADY_EXISTS / CONFIG_INVALID / BUSINESS_ERROR。 */
  error?: string
  message?: string
  /** CONFIG_INVALID 时的逐条校验问题。 */
  issues?: SchemaIssue[]
}

/** 
 * 规范化后端 `sensitivePaths` 文本为路径数组。
 * 不是合法 JSON 数组时返回空数组（展示层不因脏数据整块报错）。
 */
export function parseSensitivePaths(value: string | undefined | null): string[] {
  if (!value) return []
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed)) return []
    return parsed.map(String).filter(Boolean)
  } catch {
    return []
  }
}

/** 
 * 从错误对象里取出后端错误信封。
 * 后端所有失败都走 `respondErr` 的 `{error, message}`，故这里只做形状收敛。
 */
export function configFilesErrorBody(err: unknown): ConfigFilesErrorBody {
  if (typeof err === 'object' && err !== null && 'response' in err) {
    const data = (err as { response?: { data?: ConfigFilesErrorBody } }).response?.data
    if (data && typeof data === 'object') return data
  }
  return {}
}

/** 错误码（取不到时为空串），供调用方做定向文案。 */
export function configFilesErrorCode(err: unknown): string {
  return configFilesErrorBody(err).error ?? ''
}

// ---------------------------------------------------------------------------
// 普通函数（不带 React）：便于复用与直接单测
// ---------------------------------------------------------------------------

/** 分页列出配置文件（`q` 模糊匹配 name / description）。 */
export async function listConfigFiles(params: {
  q?: string
  page?: number
  pageSize?: number
} = {}): Promise<{ files: ConfigFileView[]; total: number; page: number; pageSize: number }> {
  const { data } = await api.get<{ files: ConfigFileView[]; total: number; page: number; pageSize: number }>(
    '/config-files',
    { params },
  )
  return data
}

/** 读取配置文件详情。 */
export async function getConfigFile(id: number): Promise<ConfigFileView> {
  const { data } = await api.get<ConfigFileView>(`/config-files/${id}`)
  return data
}

/** 创建配置文件。 */
export async function createConfigFile(input: CreateConfigFileInput): Promise<ConfigFileView> {
  const { data } = await api.post<ConfigFileView>('/config-files', input)
  return data
}

/** 修改配置文件元数据（name / format 不可改）。 */
export async function updateConfigFile(id: number, input: UpdateConfigFileInput): Promise<ConfigFileView> {
  const { data } = await api.put<ConfigFileView>(`/config-files/${id}`, input)
  return data
}

/** 软删配置文件（进回收站，层版本保留）。 */
export async function deleteConfigFile(id: number): Promise<void> {
  await api.delete(`/config-files/${id}`)
}

/** 列出各作用域的当前 head。 */
export async function listConfigFileLayers(id: number): Promise<ConfigLayerHead[]> {
  const { data } = await api.get<{ layers: ConfigLayerHead[] }>(`/config-files/${id}/layers`)
  return data.layers ?? []
}

/** 保存某作用域的新版本（追加，不改历史）。 */
export async function saveConfigFileLayer(id: number, input: SaveLayerInput): Promise<ConfigLayerVersion> {
  const { data } = await api.post<ConfigLayerVersion>(`/config-files/${id}/layers`, input)
  return data
}

/** 某作用域的版本历史（新→旧）。 */
export async function listConfigFileLayerHistory(
  id: number,
  scopeType: ConfigScopeLevel,
  scopeId: number,
): Promise<ConfigLayerVersionView[]> {
  const { data } = await api.get<{ versions: ConfigLayerVersionView[] }>(
    `/config-files/${id}/layers/${scopeType}/${scopeId}/history`,
  )
  return data.versions ?? []
}

/** 回退到指定历史版本（追加新版本，历史行不变）。 */
export async function rollbackConfigFileLayer(
  id: number,
  scopeType: ConfigScopeLevel,
  scopeId: number,
  versionId: number,
  remark?: string,
): Promise<ConfigLayerVersion> {
  const { data } = await api.post<ConfigLayerVersion>(
    `/config-files/${id}/layers/${scopeType}/${scopeId}/rollback`,
    { versionId, remark },
  )
  return data
}

/** 删除某作用域的全部版本（撤销该层覆盖）。 */
export async function deleteConfigFileLayer(
  id: number,
  scopeType: ConfigScopeLevel,
  scopeId: number,
): Promise<void> {
  await api.delete(`/config-files/${id}/layers/${scopeType}/${scopeId}`)
}

/** 某实例在该配置文件上的有效值预览（含逐键溯源）。 */
export async function getConfigFileEffective(id: number, instanceId: number): Promise<EffectiveResult> {
  const { data } = await api.get<EffectiveResult>(`/config-files/${id}/effective`, { params: { instanceId } })
  return data
}

// ---------------------------------------------------------------------------
// TanStack Query hooks
// ---------------------------------------------------------------------------

/** 配置文件列表查询键前缀（失效时统一按前缀扫）。 */
export const CONFIG_FILES_KEY = ['config-files'] as const

/** 分页列表（翻页/改关键字时保留上一批数据，避免整表闪成骨架）。 */
export function useConfigFiles(params: { q?: string; page?: number; pageSize?: number } = {}) {
  return useQuery({
    queryKey: [...CONFIG_FILES_KEY, 'list', params],
    queryFn: () => listConfigFiles(params),
    placeholderData: keepPreviousData,
  })
}

/** 配置文件详情。 */
export function useConfigFile(id: number | null) {
  return useQuery({
    queryKey: [...CONFIG_FILES_KEY, 'detail', id],
    queryFn: () => getConfigFile(id as number),
    enabled: id != null && id > 0,
  })
}

/** 各作用域当前 head。 */
export function useConfigFileLayers(id: number | null) {
  return useQuery({
    queryKey: [...CONFIG_FILES_KEY, 'layers', id],
    queryFn: () => listConfigFileLayers(id as number),
    enabled: id != null && id > 0,
  })
}

/** 某作用域的版本历史（作用域为空串时不发请求）。 */
export function useConfigFileLayerHistory(
  id: number | null,
  scopeType: ConfigScopeLevel | null,
  scopeId: number | null,
) {
  return useQuery({
    queryKey: [...CONFIG_FILES_KEY, 'history', id, scopeType, scopeId],
    queryFn: () => listConfigFileLayerHistory(id as number, scopeType as ConfigScopeLevel, scopeId as number),
    enabled: id != null && id > 0 && scopeType != null && scopeId != null && scopeId > 0,
  })
}

/** 有效值预览（未选实例时不发请求）。 */
export function useConfigFileEffective(id: number | null, instanceId: number | null) {
  return useQuery({
    queryKey: [...CONFIG_FILES_KEY, 'effective', id, instanceId],
    queryFn: () => getConfigFileEffective(id as number, instanceId as number),
    enabled: id != null && id > 0 && instanceId != null && instanceId > 0,
  })
}

/** 创建配置文件。 */
export function useCreateConfigFile() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: createConfigFile,
    onSuccess: () => qc.invalidateQueries({ queryKey: CONFIG_FILES_KEY }),
  })
}

/** 修改元数据。 */
export function useUpdateConfigFile() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, input }: { id: number; input: UpdateConfigFileInput }) => updateConfigFile(id, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: CONFIG_FILES_KEY }),
  })
}

/** 软删配置文件。 */
export function useDeleteConfigFile() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: deleteConfigFile,
    onSuccess: () => qc.invalidateQueries({ queryKey: CONFIG_FILES_KEY }),
  })
}

/** 保存层版本（新版本会改变 head、也可能改变有效值，故三处一并失效）。 */
export function useSaveConfigFileLayer(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: SaveLayerInput) => saveConfigFileLayer(id, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: CONFIG_FILES_KEY }),
  })
}

/** 回退层版本。 */
export function useRollbackConfigFileLayer(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({
      scopeType,
      scopeId,
      versionId,
      remark,
    }: {
      scopeType: ConfigScopeLevel
      scopeId: number
      versionId: number
      remark?: string
    }) => rollbackConfigFileLayer(id, scopeType, scopeId, versionId, remark),
    onSuccess: () => qc.invalidateQueries({ queryKey: CONFIG_FILES_KEY }),
  })
}

/** 删除某作用域全部版本。 */
export function useDeleteConfigFileLayer(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ scopeType, scopeId }: { scopeType: ConfigScopeLevel; scopeId: number }) =>
      deleteConfigFileLayer(id, scopeType, scopeId),
    onSuccess: () => qc.invalidateQueries({ queryKey: CONFIG_FILES_KEY }),
  })
}
