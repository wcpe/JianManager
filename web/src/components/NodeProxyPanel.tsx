import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Globe } from 'lucide-react'
import { useNodeProxy, useUpdateNodeProxy, type NodeProxyView } from '@/api/nodes'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

/**
 * 节点出站代理面板（FR-185，见 ADR-043）：单选「继承全局 / 自定义」，自定义展开 URL + no_proxy。
 *
 * 真相源 = CP DB；保存经心跳下发到 Worker，节点运行时重建出站 client（免改 worker.yaml/重启）。
 * 含凭据的代理地址后端已脱敏回显；离线节点标注「待下发」（下次心跳生效）。
 * 可复用独立组件（不绑死容器），便于挂在节点详情右栏分段。
 */
interface NodeProxyPanelProps {
  nodeId: number
  /** 是否启用查询（分段打开时为 true，避免后台轮询离屏节点）。 */
  active?: boolean
}

export default function NodeProxyPanel({ nodeId, active = true }: NodeProxyPanelProps) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useNodeProxy(nodeId, { enabled: active })
  const update = useUpdateNodeProxy(nodeId)

  if (isLoading) return <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
  if (isError || !data) return <p className="text-sm text-destructive">{t('nodeProxy.loadFailed')}</p>

  return <NodeProxyForm nodeId={nodeId} view={data} saving={update.isPending} onSave={update.mutateAsync} />
}

/** 表单主体：用 view 初始化本地草稿；切模式即时反映；自定义时才显 URL/no_proxy 输入。 */
function NodeProxyForm({
  view,
  saving,
  onSave,
}: {
  nodeId: number
  view: NodeProxyView
  saving: boolean
  onSave: (body: { mode: 'inherit' | 'custom'; url?: string; noProxy?: string }) => Promise<NodeProxyView>
}) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<'inherit' | 'custom'>(view.mode)
  // URL 草稿：脱敏回显作初值（用户改才发完整地址）；空表示未填。
  const [url, setUrl] = useState(view.url)
  const [noProxy, setNoProxy] = useState(view.noProxy)

  const effectiveText =
    view.effectiveUrl || (view.effectiveNoProxy ? '' : t('nodeProxy.effectiveDirect'))

  const save = async () => {
    try {
      await onSave(mode === 'custom' ? { mode, url, noProxy } : { mode })
      toast.success(t('nodeProxy.saved'))
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('nodeProxy.saveFailed'))
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2">
        <span className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
          <Globe className="size-4" />
        </span>
        <div>
          <h3 className="text-sm font-semibold">{t('nodeProxy.title')}</h3>
          <p className="text-xs text-muted-foreground">{t('nodeProxy.desc')}</p>
        </div>
      </div>

      {/* 当前生效 + 全局默认（脱敏） */}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-md border bg-muted/30 px-3 py-2 text-xs">
        <span className="text-muted-foreground">
          {t('nodeProxy.effective')}:{' '}
          <code className="font-mono text-foreground">{effectiveText || t('nodeProxy.effectiveDirect')}</code>
        </span>
        {!view.online && <span className="text-status-warning">{t('nodeProxy.pendingPush')}</span>}
      </div>

      {/* 模式单选：继承全局 / 自定义 */}
      <div className="space-y-1.5">
        <p className="text-xs font-medium text-muted-foreground">{t('nodeProxy.mode')}</p>
        <div className="inline-flex rounded-md border p-0.5">
          {(['inherit', 'custom'] as const).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setMode(m)}
              className={`rounded px-3 py-1 text-sm transition-colors ${
                mode === m ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              {t(`nodeProxy.${m}`)}
            </button>
          ))}
        </div>
      </div>

      {mode === 'inherit' ? (
        <p className="text-xs text-muted-foreground">
          {t('nodeProxy.inheritHint')} ·{' '}
          {view.globalDefaultUrl ? (
            <>
              {t('nodeProxy.globalDefault')}: <code className="font-mono">{view.globalDefaultUrl}</code>
            </>
          ) : (
            t('nodeProxy.globalNone')
          )}
        </p>
      ) : (
        <div className="space-y-3">
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">{t('nodeProxy.url')}</label>
            <Input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder={t('nodeProxy.urlPlaceholder')}
              className="h-8"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">{t('nodeProxy.noProxy')}</label>
            <Input
              value={noProxy}
              onChange={(e) => setNoProxy(e.target.value)}
              placeholder={t('nodeProxy.noProxyPlaceholder')}
              className="h-8"
            />
          </div>
          <p className="text-[11px] text-muted-foreground">{t('nodeProxy.maskedHint')}</p>
        </div>
      )}

      <div className="flex justify-end">
        <Button size="sm" onClick={save} disabled={saving}>
          {saving ? t('common.saving', '保存中…') : t('common.save')}
        </Button>
      </div>
    </div>
  )
}
