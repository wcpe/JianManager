import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'

/**
 * 实例配置源明面化（FR-451 / ADR-092）。
 *
 * 每个受管配置项（启动参数 `startup.*` + `server.properties` 关键项 `props.*`）显式声明来源：
 * - `inline`：平台持有该值，可编辑、可版本化（写回真源）。
 * - `file`：外部文件提供，平台不覆写，只读展示生效值预览。
 *
 * 两态互斥，杜绝「平台改了却被文件覆盖」的静默冲突。契约见 `docs/API.md`
 * （`GET/PUT /instances/:id/configs/surface`）。
 */

/** 受管配置项来源：内联值 / 文件引用（二态互斥）。 */
export type ConfigSourceKind = 'inline' | 'file'

/** 受管配置项清单中的一行。 */
export interface ConfigSurfaceItem {
  itemKey: string
  source: ConfigSourceKind
  /** 是否已在登记表显式声明；false=旧实例的隐含默认（启动项内联 / props 文件隐含）。 */
  registered: boolean
  inlineValue?: string
  filePath?: string
  fileKey?: string
  /** 生效值：file 项为解析预览，inline 项即值本身。 */
  effectiveValue: string
  effectiveSource: 'inline' | 'file'
  editable: boolean
  previewError?: string
  /** 分组（如「启动参数」「网络与身份」），供 UI 分节。 */
  group?: string
  description?: string
  /** 字段类型（bool / int / string / enum…），供表单渲染控件。 */
  type?: string
  choices?: string[]
}

/** 按项更新来源的请求体。 */
export interface ConfigSurfaceUpdateItem {
  itemKey: string
  source: ConfigSourceKind
  /** 显式内联值；留空时后端按迁移语义推导（file→inline 取文件现值）。 */
  inlineValue?: string
  filePath?: string
  fileKey?: string
  message?: string
}

/** 查询实例受管配置项清单（FR-451）。 */
export function useConfigSurface(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['config-surface', instanceId],
    queryFn: async () => {
      const { data } = await api.get<{ items: ConfigSurfaceItem[] }>(`/instances/${instanceId}/configs/surface`)
      return data.items
    },
    enabled: enabled && !!instanceId,
  })
}

/** 按项更新配置源（FR-451）：二态互斥、内联写回真源、文件引用仅登记 + 生效值预览。 */
export function useUpdateConfigSurface(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (items: ConfigSurfaceUpdateItem[]) => {
      const { data } = await api.put<{ items: ConfigSurfaceItem[] }>(
        `/instances/${instanceId}/configs/surface`,
        { items },
      )
      return data.items
    },
    onSuccess: () => {
      toast.success('配置源已保存')
      qc.invalidateQueries({ queryKey: ['config-surface', instanceId] })
      qc.invalidateQueries({ queryKey: ['configs', instanceId] })
      qc.invalidateQueries({ queryKey: ['instances', instanceId] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '保存配置源失败')
    },
  })
}
