import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Loader2, Check } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { toast } from 'sonner'
import { useNodePMConfig, useSetNodePMConfig, type PMRegistry } from '@/api/pmConfig'

interface NodePMConfigSectionProps {
  nodeId: number
  active?: boolean
}

const PMS = ['npm', 'pnpm', 'yarn'] as const

/**
 * 节点包管理器与 registry 配置子区（FR-306）。选 npm/pnpm/yarn（corepack 激活 pnpm/yarn）+
 * 多 registry（默认源 + @scope 域源 + 可选凭据，凭据脱敏）。落节点托管 .npmrc。
 */
export default function NodePMConfigSection({ nodeId, active = true }: NodePMConfigSectionProps) {
  const { t } = useTranslation()
  const { data, isLoading } = useNodePMConfig(nodeId, { enabled: active })
  const save = useSetNodePMConfig(nodeId)

  const [pm, setPm] = useState<string>('npm')
  const [regs, setRegs] = useState<PMRegistry[]>([])
  // 渲染期同步 query 数据 → 表单草稿（React 官方模式，避免 effect 内 setState）：
  // data 引用变化时重置一次；用户后续编辑不被覆盖。
  const [syncedData, setSyncedData] = useState<typeof data>(undefined)
  if (data && data !== syncedData) {
    setSyncedData(data)
    setPm(data.pm)
    // registries 判空守卫：空配置节点后端可回 null（Go nil 切片序列化），直接 .length 会
    // TypeError 拖垮整个节点页（v0.15.0 真机白屏根因）；null/空一律给一条空行可编辑。
    const regs = data.registries ?? []
    setRegs(regs.length > 0 ? regs : [{ url: '', scope: '' }])
  }

  if (!active) return null
  if (isLoading)
    // 骨架占位：标题行 + 包管理器选择行 + registry 输入行轮廓，替代裸文字。
    return (
      <div className="space-y-2">
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </div>
    )

  const setReg = (i: number, patch: Partial<PMRegistry>) =>
    setRegs((prev) => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))
  const addReg = () => setRegs((prev) => [...prev, { url: '', scope: '' }])
  const removeReg = (i: number) => setRegs((prev) => prev.filter((_, idx) => idx !== i))

  const onSave = () => {
    const cleaned = regs.filter((r) => r.url.trim() !== '')
    save.mutate(
      { pm, registries: cleaned },
      {
        onSuccess: () => toast.success(t('pmConfig.saved', '包管理器配置已保存')),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e.response?.data?.message || t('pmConfig.saveFailed', '保存失败')),
      },
    )
  }

  return (
    <div className="space-y-3 rounded-lg border p-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">{t('pmConfig.title', '包管理器与下载源')}</span>
        {data?.corepackAvailable === false && (
          <span className="text-xs text-yellow-600">{t('pmConfig.noCorepack', '托管 Node 无 corepack，仅 npm 可用')}</span>
        )}
      </div>

      <div>
        <span className="mb-1 block text-xs text-muted-foreground">{t('pmConfig.pmLabel', '包管理器')}</span>
        <div className="flex gap-2">
          {PMS.map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => setPm(p)}
              className={`flex items-center gap-1 rounded-md border px-3 py-1 text-sm ${
                pm === p ? 'border-primary bg-primary/5 font-medium' : 'hover:bg-accent/50'
              }`}
            >
              {pm === p && <Check className="size-3.5" />}
              {p}
              {p === data?.pm && data?.pmVersion ? ` (${data.pmVersion})` : ''}
            </button>
          ))}
        </div>
      </div>

      <div>
        <div className="mb-1 flex items-center justify-between">
          <span className="text-xs text-muted-foreground">{t('pmConfig.registries', '下载源 registry（首条为默认源，其余按 @scope 域）')}</span>
          <Button size="sm" variant="ghost" onClick={addReg}>
            <Plus className="size-3.5" /> {t('pmConfig.addRegistry', '添加源')}
          </Button>
        </div>
        <div className="space-y-1.5">
          {regs.map((r, i) => (
            <div key={i} className="flex flex-wrap items-center gap-1.5">
              <Input
                value={r.scope ?? ''}
                onChange={(e) => setReg(i, { scope: e.target.value })}
                placeholder={i === 0 ? t('pmConfig.defaultScope', '默认源（scope 留空）') : '@scope'}
                className="h-8 w-28 text-xs"
                aria-label={t('pmConfig.scope', 'scope')}
              />
              <Input
                value={r.url}
                onChange={(e) => setReg(i, { url: e.target.value })}
                placeholder="https://registry.npmmirror.com"
                className="h-8 min-w-0 flex-1 text-xs"
                aria-label={t('pmConfig.registryUrl', 'registry 地址')}
              />
              <Input
                value={r.token ?? ''}
                onChange={(e) => setReg(i, { token: e.target.value, tokenMasked: false })}
                placeholder={r.tokenMasked ? '********' : t('pmConfig.tokenOptional', '凭据(可选)')}
                type="password"
                className="h-8 w-28 text-xs"
                aria-label={t('pmConfig.token', '凭据')}
              />
              <Button size="sm" variant="ghost" onClick={() => removeReg(i)} aria-label={t('common.delete', '删除')}>
                <Trash2 className="size-3.5" />
              </Button>
            </div>
          ))}
        </div>
      </div>

      <div className="flex justify-end">
        <Button size="sm" onClick={onSave} disabled={save.isPending}>
          {save.isPending ? <Loader2 className="size-4 animate-spin" /> : null}
          {t('pmConfig.save', '保存配置')}
        </Button>
      </div>
    </div>
  )
}
