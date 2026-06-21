import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 单个平台配置项（FR-063 / ADR-015）。
 * 敏感项的 value 已由后端脱敏，不含明文。
 */
export interface SettingItem {
  /** 配置键，如 log.level、graceful_stop.timeout。 */
  key: string
  /** 当前生效值（DB 覆盖 > env > YAML），敏感项已脱敏。 */
  value: string
  /** 是否可经 PUT /settings 运行时修改。 */
  editable: boolean
  /** 是否敏感项（值已脱敏）。 */
  sensitive: boolean
  /** 该项当前是否被 DB 覆盖（仅可编辑项有意义）。 */
  overridden: boolean
  /** 运行时修改是否在 Control Plane 内即时生效（否则需改配置/重启或在 Worker 侧生效）。 */
  effectiveImmediately: boolean
}

/** GET /settings 响应：可编辑项与只读项分区。 */
export interface SettingsView {
  editable: SettingItem[]
  readOnly: SettingItem[]
}

/** PUT /settings 请求体：一次提交一批「键 → 覆盖值」。 */
export interface UpdateSettingsBody {
  values: Record<string, string>
}

/** 读取平台配置全量视图（仅平台管理员可访问）。 */
export function useSettings() {
  return useQuery({
    queryKey: ['settings'],
    queryFn: async () => {
      const { data } = await api.get<SettingsView>('/settings')
      return data
    },
  })
}

/** 写入平台配置覆盖；成功后失效缓存以回填最新有效值。 */
export function useUpdateSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: UpdateSettingsBody) => {
      const { data } = await api.put<SettingsView>('/settings', body)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings'] }),
  })
}
