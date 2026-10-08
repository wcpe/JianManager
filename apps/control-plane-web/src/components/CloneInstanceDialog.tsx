// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做代理候选取数、预检/提交两次写请求与提示文案。
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useInstances } from '@/api/instances'
import { useCloneInstance } from '@/api/clone'
import CloneInstanceDialogView, {
  type CloneFormDraft,
} from '@jianmanager/ui/components/views/instances/CloneInstanceDialogView'

/** 把逗号/换行分隔的 glob 串解析为数组（FR-231 高级复制筛选）。 */
const parseGlobs = (s: string) => s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean)

/** 复制端点的错误形态（`error` 码 + 服务端 message）。 */
type CloneError = Error & { response?: { data?: { error?: string; message?: string } } }

interface CloneInstanceDialogProps {
  sourceId: number
  sourceName: string
  onClose: () => void
}

/**
 * 复制子服向导接线层（ADR-097 b 范式，原 FR-036 一键复制 / FR-231 高级筛选）。
 *
 * 视图本体已迁入组件库并受控（不取数、不发请求、不弹 toast）；本层负责：
 * - 代理候选：`useInstances({ role: 'proxy' })` 取数后注入（原实现同源）；
 * - 「草稿 → 请求体」组装：`motd`/`levelName` 去空白后留空即不下发，`include`/`exclude` 仅在
 *   advanced 模式下解析为 glob 数组（请求语义留应用侧，视图只给表单原文）；
 * - 两次写请求（预检 dryRun / 正试）与错误文案：`SOURCE_RUNNING` 有专属文案，其余取服务端 message；
 * - 成功文案与逐条告警 toast（视图不提示，避免双重 toast）。
 *
 * 保留同路径默认导出与同名 props，调用点（InstancesPage）无需改动。
 */
export default function CloneInstanceDialog({ sourceId, sourceName, onClose }: CloneInstanceDialogProps) {
  const { t } = useTranslation()
  const { data: proxies } = useInstances({ role: 'proxy' })
  const clone = useCloneInstance(sourceId)

  /** 草稿 → 请求体（字段省略规则与原实现逐字一致）。 */
  const toBody = (draft: CloneFormDraft) => ({
    name: draft.name,
    motd: draft.motd.trim() || undefined,
    levelName: draft.levelName.trim() || undefined,
    registerToProxyIds: draft.registerToProxyIds.length ? draft.registerToProxyIds : undefined,
    mode: draft.mode,
    include: draft.mode === 'advanced' ? parseGlobs(draft.include) : undefined,
    exclude: draft.mode === 'advanced' ? parseGlobs(draft.exclude) : undefined,
  })

  /** 失败提示：`SOURCE_RUNNING`（源实例运行中）有专属文案，其余取服务端 message，兜底 clone.failed。 */
  const notifyError = (err: CloneError) => {
    const code = err.response?.data?.error
    toast.error(code === 'SOURCE_RUNNING' ? t('clone.sourceRunning') : err.response?.data?.message || t('clone.failed'))
  }

  return (
    <CloneInstanceDialogView
      sourceName={sourceName}
      proxies={proxies}
      submitting={clone.isPending}
      onClose={onClose}
      onPreview={async (draft) => {
        try {
          const res = await clone.mutateAsync({ ...toBody(draft), dryRun: true })
          return res
        } catch (err) {
          notifyError(err as CloneError)
          // 失败返回 null：视图不渲染预览块（原实现同样只提示、不改预览）。
          return null
        }
      }}
      onSubmit={async (draft) => {
        try {
          const res = await clone.mutateAsync(toBody(draft))
          toast.success(t('clone.success', { name: draft.name }))
          ;(res.warnings || []).forEach((w) => toast.warning(w))
          return true
        } catch (err) {
          notifyError(err as CloneError)
          return false
        }
      }}
    />
  )
}
