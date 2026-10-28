import { useState } from 'react'
import { AlertTriangle, Link2, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
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
 * 受控视图（ADR-103 b 范式）：数据经 props 注入，保存以 `onSave` 上报，成功/失败文案由外壳决定。
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
      {/* 标题与说明留在外层 Panel 的标题栏与 desc 里，这里只保留说明文字，避免同名标题渲染两遍。 */}
      <p className="text-xs text-muted-foreground">{t('nodeProxy.desc')}</p>

      {/* 当前生效 + 全局默认（脱敏） */}
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border bg-muted/30 px-3 py-2 text-xs">
        <Link2 className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="text-muted-foreground">
          {t('nodeProxy.effective')}:{' '}
          <code className="font-mono font-semibold text-foreground">{effectiveText || t('nodeProxy.effectiveDirect')}</code>
        </span>
      </div>
      {/*
        离线时的「待下发」原先只是一行 11px 小字，和说明挤在一起极易漏看。
        它是「改了但还没生效」的唯一提示，漏看就会以为配置没保存成功，故独立成警示条。
      */}
      {!view.online && (
        <div className="flex items-start gap-1.5 rounded-md border border-status-warning/40 bg-status-warning/5 px-3 py-2 text-[11px] text-status-warning">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          {t('nodeProxy.pendingPush')}
        </div>
      )}

      {/* 安全边界：这条决定运维敢不敢动代理——配错不会把节点弄失联。 */}
      <p className="flex items-start gap-1.5 rounded-md border border-status-success/30 bg-status-success/5 px-3 py-2 text-[11px] text-muted-foreground">
        <ShieldCheck className="mt-0.5 size-3.5 shrink-0 text-status-success" aria-hidden />
        {t('nodes.proxySafety')}
      </p>

      {/* 模式单选：继承全局 / 自定义 */}
      <div className="space-y-1.5">
        <p className="text-xs font-medium text-muted-foreground">{t('nodeProxy.mode')}</p>
        <Segments aria-label={t('nodeProxy.mode')}>
          {(['inherit', 'custom'] as const).map((m) => (
            <Segment key={m} active={mode === m} onClick={() => setMode(m)}>
              {t(`nodeProxy.${m}`)}
            </Segment>
          ))}
        </Segments>
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
