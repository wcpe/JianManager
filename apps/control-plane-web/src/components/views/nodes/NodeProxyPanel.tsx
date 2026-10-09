import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Globe } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Skeleton } from '@jianmanager/ui/components/skeleton'

/**
 * 节点出站代理视图（本组件所需的最小结构）。
 *
 * 外壳直接传 `@/api/nodes` 的 `NodeProxyView` 会结构兼容，故无需把该 API 类型迁进包。
 */
export interface NodeProxyView {
  /** inherit=继承全局默认 / custom=自定义。 */
  mode: 'inherit' | 'custom'
  /** 节点自定义代理地址（脱敏；仅 custom 有意义）。 */
  url: string
  /** 节点自定义免代理列表（逗号分隔；仅 custom 有意义）。 */
  noProxy: string
  /** 当前生效代理地址（脱敏）：custom→节点值，inherit→全局默认。 */
  effectiveUrl: string
  /** 当前生效免代理列表。 */
  effectiveNoProxy: string
  /** 平台全局默认代理地址（脱敏，供展示「继承自全局」）。 */
  globalDefaultUrl: string
  /** 节点是否在线：离线时保存的结果要等下次心跳才生效，需在界面标注。 */
  online: boolean
}

/**
 * 节点出站代理面板（FR-185，见 ADR-043）：单选「继承全局 / 自定义」，自定义展开 URL + no_proxy。
 *
 * 真相源 = CP DB；保存经心跳下发到 Worker，节点运行时重建出站 client（免改 worker.yml/重启）。
 * 含凭据的代理地址后端已脱敏回显；离线节点标注「待下发」（下次心跳生效）。
 *
 * 受控视图（ADR-097 b 范式）：数据经 props 注入，保存以 `onSave` 上报，成功/失败文案由外壳决定。
 */
export interface NodeProxyPanelProps {
  /** 代理视图；外壳取数后注入。 */
  data?: NodeProxyView
  /** 加载态。 */
  isLoading?: boolean
  /** 错误态（请求失败或无数据）。 */
  isError?: boolean
  /** 保存在途：禁用保存按钮。 */
  saving?: boolean
  /** 保存代理配置。返回是否成功（组件不关心后续提示）。 */
  onSave: (body: { mode: 'inherit' | 'custom'; url?: string; noProxy?: string }) => Promise<boolean>
}

export default function NodeProxyPanel({
  data,
  isLoading,
  isError,
  saving = false,
  onSave,
}: NodeProxyPanelProps) {
  const { t } = useTranslation()

  if (isLoading)
    // 骨架占位：标题行 + 生效信息条 + 模式行轮廓，替代裸文字避免布局跳动。
    return (
      <div className="space-y-3">
        <Skeleton className="h-5 w-44" />
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-9 w-2/3" />
      </div>
    )
  if (isError || !data) return <p className="text-sm text-destructive">{t('nodeProxy.loadFailed')}</p>

  return <NodeProxyForm view={data} saving={saving} onSave={onSave} />
}

/** 表单主体：用 view 初始化本地草稿；切模式即时反映；自定义时才显 URL/no_proxy 输入。 */
function NodeProxyForm({
  view,
  saving,
  onSave,
}: {
  view: NodeProxyView
  saving: boolean
  onSave: (body: { mode: 'inherit' | 'custom'; url?: string; noProxy?: string }) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<'inherit' | 'custom'>(view.mode)
  // URL 草稿：脱敏回显作初值（用户改才发完整地址）；空表示未填。
  const [url, setUrl] = useState(view.url)
  const [noProxy, setNoProxy] = useState(view.noProxy)

  const effectiveText =
    view.effectiveUrl || (view.effectiveNoProxy ? '' : t('nodeProxy.effectiveDirect'))

  // 保存结果由外壳负责提示；组件保留草稿，使用户可在失败后直接重试。
  const save = async () => {
    await onSave(mode === 'custom' ? { mode, url, noProxy } : { mode })
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
