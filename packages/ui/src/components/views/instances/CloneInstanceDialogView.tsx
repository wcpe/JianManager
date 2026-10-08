/**
 * @file CloneInstanceDialogView：一键复制子服向导（克隆选项 + 预检结果）的受控视图，代理候选与两次写请求由应用容器负责。
 * @input form-validation（validateRequired 必填校验）、Button/Checkbox/Dialog/scrollable-dialog 原语、
 *        FieldLabel/FieldError、cn、翻译上下文
 * @output CloneInstanceDialogView、CloneInstanceDialogViewProps、CloneFormDraft、ClonePreview、CloneProxyOption
 * @sync apps/control-plane-web/src/components/CloneInstanceDialog.tsx、
 *        apps/control-plane-web/src/components/CloneInstanceDialog.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-036 一键复制、FR-231 高级复制筛选、FR-202 供给/部署流程）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateRequired } from '@jianmanager/ui/lib/form-validation'
import { cn } from '@jianmanager/ui'

/** 可登记进代理的候选（视图只需要 id 与展示名；应用侧传完整实例对象亦结构兼容）。 */
export interface CloneProxyOption {
  id: number
  name: string
}

/**
 * 表单草稿（**原文**，非请求体）：`include`/`exclude` 保持用户输入的逗号/换行串，
 * 由容器解析为 glob 数组并按 `mode` 决定是否下发——「草稿 → 请求体」属请求语义，留在应用侧。
 */
export interface CloneFormDraft {
  /** 新实例名（必填）。 */
  name: string
  /** 新实例 motd（留空表示不改写）。 */
  motd: string
  /** 新实例 level-name（留空表示不改写）。 */
  levelName: string
  /** 要登记进的代理 id 列表（空数组表示不登记）。 */
  registerToProxyIds: number[]
  /** 复制模式（FR-231）：quick=核心+插件+根配置；advanced=按 include/exclude 筛选。 */
  mode: 'quick' | 'advanced'
  /** 高级模式包含项原文（逗号/换行分隔的顶层项）。 */
  include: string
  /** 高级模式排除项原文。 */
  exclude: string
}

/** 预检（dryRun）结果里视图需要展示的部分（`CloneResult` 结构兼容，无需迁 API 类型进包）。 */
export interface ClonePreview {
  /** 将分配的资源。 */
  allocated: { serverPort: number; queryPort: number; workDir: string }
  /** 已排除的顶层项。 */
  excluded: string[]
  /** 非阻断告警（提交成功后由容器逐条提示）。 */
  warnings?: string[]
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 代理候选经 `proxies` 注入（容器调 `useInstances({ role: 'proxy' })`）；
 * - 预检与提交分别经 `onPreview` / `onSubmit` 上报：`onPreview` 回传结果（失败返回 null，容器已提示），
 *   结果只用于本视图的预览块；`onSubmit` 回传是否成功——成功才关窗（失败保留草稿便于重试），
 *   成功文案与告警由容器负责；
 * - **留本视图**的状态：全部表单草稿（名称/motd/level-name/代理勾选/复制模式/include/exclude）
 *   与预检结果。它们都不触发取数，且勾选与输入属纯编辑态；
 * - 开合：与原实现一致——**挂载即显示**（无 `open` prop），取消/遮罩/Esc/提交成功均走 `onClose`。
 */
export interface CloneInstanceDialogViewProps {
  /** 来源实例展示名（标题与默认新实例名用）。 */
  sourceName: string
  /** 可登记的代理候选；缺省或空数组渲染「无可用代理」。 */
  proxies?: CloneProxyOption[]
  /** 提交在途（禁用预检与提交按钮，并显示复制中文案）。 */
  submitting?: boolean
  /** 关闭对话框。 */
  onClose: () => void
  /** 预检（dryRun）：返回预检结果；失败返回 null（容器负责提示），视图不展示错误块。 */
  onPreview: (draft: CloneFormDraft) => Promise<ClonePreview | null>
  /** 提交复制；返回是否成功——成功才关窗（失败保留草稿与窗口便于重试，提示由容器负责）。 */
  onSubmit: (draft: CloneFormDraft) => Promise<boolean>
}

/**
 * 一键复制子服向导（FR-036）：复制为独立新实例，系统分配新目录/端口，排除运行态文件，
 * 修正端口/motd 并可选注册进代理。支持预检（dryRun）。
 */
export default function CloneInstanceDialogView({
  sourceName,
  proxies,
  submitting = false,
  onClose,
  onPreview,
  onSubmit,
}: CloneInstanceDialogViewProps) {
  const { t } = useTranslation()

  const [name, setName] = useState(`${sourceName}-copy`)
  const [motd, setMotd] = useState('')
  const [levelName, setLevelName] = useState('')
  const [proxyIds, setProxyIds] = useState<number[]>([])
  const [preview, setPreview] = useState<ClonePreview | null>(null)
  // 复制模式（FR-231）：quick=核心+插件+根配置；advanced=按 include/exclude 筛选。
  const [mode, setMode] = useState<'quick' | 'advanced'>('quick')
  const [include, setInclude] = useState('')
  const [exclude, setExclude] = useState('')

  const nameError = validateRequired(name)

  /** 当前草稿（原文）；请求体组装在容器完成。 */
  const draft = (): CloneFormDraft => ({ name, motd, levelName, registerToProxyIds: proxyIds, mode, include, exclude })

  const runPreview = async () => {
    const res = await onPreview(draft())
    if (res) setPreview(res)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (nameError) return
    if (await onSubmit(draft())) onClose()
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent
        className={`${scrollableDialogContentClass} sm:max-w-md`}
        showCloseButton={false}
        onInteractOutside={(event) => event.preventDefault()}
      >
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader>
            <DialogTitle>{t('clone.title', { name: sourceName })}</DialogTitle>
          </DialogHeader>

          <ScrollableDialogBody className="space-y-3 py-2">
            <div>
              <FieldLabel required>{t('clone.name')}</FieldLabel>
              <input value={name} onChange={(e) => setName(e.target.value)}
                aria-invalid={!!nameError}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" />
              <FieldError error={nameError} />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <FieldLabel>{t('clone.motd')}</FieldLabel>
                <input value={motd} onChange={(e) => setMotd(e.target.value)}
                  className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" />
              </div>
              <div>
                <FieldLabel>{t('clone.levelName')}</FieldLabel>
                <input value={levelName} onChange={(e) => setLevelName(e.target.value)} placeholder="world"
                  className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" />
              </div>
            </div>

            <div>
              <FieldLabel>{t('clone.registerTo')}</FieldLabel>
              {proxies && proxies.length > 0 ? (
                <div className="mt-1 border rounded-md p-2 space-y-1 max-h-32 overflow-y-auto">
                  {proxies.map((p) => (
                    <label key={p.id} className="flex items-center gap-2 text-sm">
                      <Checkbox checked={proxyIds.includes(p.id)} aria-label={p.name}
                        onCheckedChange={(v) => setProxyIds((prev) => (v === true ? [...prev, p.id] : prev.filter((x) => x !== p.id)))} />
                      {p.name}
                    </label>
                  ))}
                </div>
              ) : (
                <p className="mt-1 text-xs text-muted-foreground">{t('clone.noProxies')}</p>
              )}
            </div>

            {/* 复制范围：快速 / 高级（FR-231） */}
            <div>
              <FieldLabel>{t('clone.mode', '复制范围')}</FieldLabel>
              <div className="mt-1 flex gap-1 rounded-md border bg-muted/30 p-1 text-sm">
                {(['quick', 'advanced'] as const).map((m) => (
                  <button
                    key={m}
                    type="button"
                    onClick={() => setMode(m)}
                    className={cn(
                      'flex-1 rounded px-2 py-1.5 transition-colors',
                      mode === m ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground hover:text-foreground',
                    )}
                  >
                    {t(`clone.mode_${m}`, m === 'quick' ? '快速复制' : '高级复制')}
                  </button>
                ))}
              </div>
              {mode === 'quick' ? (
                <p className="mt-1 text-xs text-muted-foreground">{t('clone.quickHint', '仅复制核心 jar + 所有插件 + 根配置（server.properties 及根 *.yml/*.properties），不含世界 / 日志 / 缓存。')}</p>
              ) : (
                <div className="mt-2 space-y-2">
                  <div>
                    <FieldLabel>{t('clone.include', '包含（顶层项，留空=全部）')}</FieldLabel>
                    <input value={include} onChange={(e) => setInclude(e.target.value)} placeholder="plugins, *.jar, world"
                      className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" />
                  </div>
                  <div>
                    <FieldLabel>{t('clone.exclude', '排除（顶层项）')}</FieldLabel>
                    <input value={exclude} onChange={(e) => setExclude(e.target.value)} placeholder="world, dynmap"
                      className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" />
                  </div>
                  <p className="text-xs text-muted-foreground">{t('clone.advancedHint', '按逗号 / 换行分隔的顶层项（目录名或 *.jar 等通配）；运行态垃圾始终排除。')}</p>
                </div>
              )}
            </div>

            {preview && (
              <div className="text-xs bg-muted/40 rounded-md p-3 space-y-1">
                <p>{t('clone.allocated', {
                  server: preview.allocated.serverPort,
                  query: preview.allocated.queryPort,
                  workDir: preview.allocated.workDir,
                })}</p>
                <p className="text-muted-foreground">{t('clone.excluded', { list: preview.excluded.join(', ') })}</p>
                {(preview.warnings || []).map((w, i) => (<p key={i} className="text-amber-600">⚠ {w}</p>))}
              </div>
            )}
          </ScrollableDialogBody>

          <DialogFooter className="flex-row justify-end pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="button" variant="outline" onClick={() => void runPreview()} disabled={submitting || !name}>
              {t('clone.preview')}
            </Button>
            <Button type="submit" disabled={submitting || !name}>
              {submitting ? t('clone.cloning') : t('clone.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
