import { useCallback, useEffect, useMemo, useState, type ChangeEvent } from 'react'
import { useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { unzip, type Unzipped } from 'fflate'
import {
  Upload,
  Trash2,
  Loader2,
  ArrowLeft,
  ArrowRight,
  Check,
  ChevronLeft,
  FileArchive,
  FileIcon,
} from 'lucide-react'
import {
  usePublishClientFile,
  usePublishClientVersion,
  type ManifestFile,
} from '@/api/clientVersions'
import {
  PUBLISH_STEPS,
  canAdvance,
  canPublish,
  nextStep,
  prevStep,
  parseManagedDirs,
  normalizeManifestPath,
  isZipFilename,
  hasPublishDraft,
  type PublishStepId,
} from '@/lib/client-publish-wizard'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import DangerConfirm from '@/components/DangerConfirm'
import ClientFileTree from '@/components/ClientFileTree'
import FileBrowser from '@/components/file-browser/FileBrowser'
import { clientDistSource } from '@/components/file-browser/sources/clientDistSource'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/** 发布向导草稿文件项（artifact 元数据 + 原始内容元数据；codec=none 时两者同值）。 */
interface DraftFile {
  filename: string
  path: string
  sync: ManifestFile['sync']
  platform: ManifestFile['platform']
  sha256: string
  md5: string
  size: number
  codec: string
}

/** 向导步骤的标题 i18n 键（顺序与 PUBLISH_STEPS 对齐）。 */
const PUBLISH_STEP_META: Record<PublishStepId, { key: string; fallback: string }> = {
  files: { key: 'clientVersions.stepFiles', fallback: '选择文件' },
  configure: { key: 'clientVersions.stepConfigure', fallback: '逐文件配置' },
  meta: { key: 'clientVersions.stepMeta', fallback: '托管目录 / 说明' },
  review: { key: 'clientVersions.stepReview', fallback: '预览发布' },
}

/**
 * 客户端分发「发布新版本」独立页面（FR-191，纠正 FR-187 的模态向导）。
 *
 * 原 PublishWizard 是 {@link ClientVersionsPanel} 内的模态 Dialog——真机暴露「点遮罩/Esc
 * 直接关闭、丢上传草稿」。FR-191 改为独立路由页 `/client-channels/:id/publish`：页面级不存在
 * 「点外面关闭」，从根上消除误关丢草稿；离开页（返回/路由切换/刷新关页）有未发布草稿时二次确认拦截。
 *
 * 分步编排：选文件 → 逐文件配置 path/sync/platform → 托管目录/说明 → 预览 → 发布。
 * 复用 usePublishClientFile/usePublishClientVersion 与后端 `POST .../files`、`POST .../versions`，
 * 不改后端。上传 codec=none（服务端入库即算 sha256/md5/size 自动填充）。
 */
export default function ClientPublishPage() {
  const { t } = useTranslation()
  const { id: channelId } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const uploadFile = usePublishClientFile()
  const publish = usePublishClientVersion()

  const [step, setStep] = useState<PublishStepId>('files')
  const [drafts, setDrafts] = useState<DraftFile[]>([])
  const [managedDirs, setManagedDirs] = useState('mods, config, resourcepacks')
  const [note, setNote] = useState('')
  const [uploading, setUploading] = useState(false)
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null)
  // 已确认离开（发布成功 / 取消确认 / 通过 blocker 确认）——置位即解除离开守卫，
  // 放行后续 backToVersions 导航不再被 useBlocker 二次拦截。
  const [leaving, setLeaving] = useState(false)
  // 点「取消」时若有草稿，开此确认弹窗（useBlocker 只拦路由导航，取消是页内显式动作需另起确认）。
  const [manualDiscard, setManualDiscard] = useState(false)

  // 有已上传草稿即「有未发布草稿」——离开页需二次确认（FR-191 离开守卫）。确认离开后解除。
  const dirty = hasPublishDraft(drafts.length) && !leaving

  /** 解除守卫并返回频道工作台版本 tab（取消确认 / 发布成功后）。 */
  const leaveToVersions = useCallback(() => {
    setLeaving(true)
  }, [])

  // leaving 置位后 dirty 转 false、blocker 解除，再导航回工作台（在 effect 中等状态落定）。
  useEffect(() => {
    if (leaving) navigate(`/client-channels?channel=${encodeURIComponent(channelId ?? '')}&tab=versions`)
  }, [leaving, navigate, channelId])

  // 离开守卫（FR-191）：本应用用 BrowserRouter（非 data router），useBlocker 不可用，
  // 改用 History API 守卫浏览器后退——有草稿时拦后退、转二次确认；关页/刷新见下方 beforeunload。
  const [navBlocked, setNavBlocked] = useState(false)
  useEffect(() => {
    if (!dirty) return
    // 压入一枚哨兵历史项，使首次「后退」停留在本页，转而弹确认。
    window.history.pushState(null, '', window.location.href)
    const onPopState = () => {
      window.history.pushState(null, '', window.location.href)
      setNavBlocked(true)
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [dirty])

  // 关页/刷新守卫：有草稿时触发浏览器原生离开确认（beforeunload 无法自定义文案）。
  useEffect(() => {
    if (!dirty) return
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [dirty])

  /** 缺失 channelId（异常直链）兜底：回频道列表。 */
  useEffect(() => {
    if (!channelId) navigate('/client-channels', { replace: true })
  }, [channelId, navigate])

  /** 上传一个文件，得内容寻址元数据后追加一条草稿（path 由调用方给定）。 */
  const uploadOne = async (file: File, path: string) => {
    const res = await uploadFile.mutateAsync({ channelId: channelId!, file, codec: 'none' })
    setDrafts((prev) => [
      ...prev,
      {
        filename: file.name,
        path,
        sync: 'strict',
        platform: '',
        sha256: res.sha256,
        md5: res.md5,
        size: res.size,
        codec: res.codec,
      },
    ])
  }

  /**
   * 解包 zip（fflate 客户端解包），逐 entry 上传，path 取自 zip 内相对路径（POSIX 归一）。
   * 跳过目录项与 __MACOSX 噪音；返回 entry 名→字节的有序数组。
   */
  const unzipEntries = (data: Uint8Array): Promise<Array<{ name: string; bytes: Uint8Array }>> =>
    new Promise((resolve, reject) => {
      unzip(data, (err, unzipped: Unzipped) => {
        if (err) return reject(err)
        const out: Array<{ name: string; bytes: Uint8Array }> = []
        for (const [name, bytes] of Object.entries(unzipped)) {
          if (name.endsWith('/')) continue // 目录项
          if (name.startsWith('__MACOSX/') || name.endsWith('.DS_Store')) continue
          out.push({ name, bytes })
        }
        resolve(out)
      })
    })

  const onPickFiles = async (e: ChangeEvent<HTMLInputElement>) => {
    const picked = Array.from(e.target.files ?? [])
    e.target.value = '' // 允许重复选择同名文件再次触发 change
    if (picked.length === 0) return
    setUploading(true)
    try {
      // 先展开 zip → 得到「待上传单元」总表，便于统一进度。
      const units: Array<{ file: File; path: string }> = []
      for (const f of picked) {
        if (isZipFilename(f.name)) {
          const buf = new Uint8Array(await f.arrayBuffer())
          const entries = await unzipEntries(buf)
          for (const ent of entries) {
            const path = normalizeManifestPath(ent.name)
            if (path === '') continue
            const base = path.split('/').pop() || 'file'
            // copy 进独立 ArrayBuffer，避免 File 持有可变视图。
            units.push({ file: new File([ent.bytes.slice().buffer], base), path })
          }
        } else {
          units.push({ file: f, path: f.name })
        }
      }
      setProgress({ done: 0, total: units.length })
      for (let i = 0; i < units.length; i++) {
        await uploadOne(units[i].file, units[i].path)
        setProgress({ done: i + 1, total: units.length })
      }
    } catch (err) {
      toast.error(errMsg(err, t('clientVersions.uploadFailed', '上传文件失败')))
    } finally {
      setUploading(false)
      setProgress(null)
    }
  }

  const patchDraft = (i: number, patch: Partial<DraftFile>) =>
    setDrafts((prev) => prev.map((d, idx) => (idx === i ? { ...d, ...patch } : d)))

  const removeDraft = (i: number) => setDrafts((prev) => prev.filter((_, idx) => idx !== i))

  const wizardState = { draftCount: drafts.length, paths: drafts.map((d) => d.path), uploading }
  const parsedDirs = parseManagedDirs(managedDirs)
  const publishable = canPublish(wizardState) && !publish.isPending

  // 预览（FR-214）：把草稿映射为客户端分发数据源——草稿 codec=none，其 sha256 即 artifact sha。
  // 经管理面 JWT 制品内容端点取文本（与玩家拉取密钥端点隔离，见 ADR-022/023）。
  const previewSource = useMemo(
    () =>
      clientDistSource(
        channelId ?? '',
        drafts.map((d) => ({ path: d.path, size: d.size, artifactSha: d.sha256 })),
      ),
    [channelId, drafts],
  )
  // 预览步骤的视图（结构 = ClientFileTree 编排预览；预览 = 共享 FileBrowser 看内容）。
  const [reviewView, setReviewView] = useState<'structure' | 'preview'>('structure')

  /** 尝试取消：有草稿弹二次确认，无草稿直接回工作台。 */
  const attemptCancel = () => {
    if (dirty) setManualDiscard(true)
    else leaveToVersions()
  }

  const doPublish = async () => {
    if (!publishable) return
    // codec=none：file 原始内容元数据 = artifact 元数据（上传的就是原始文件）。
    const files: ManifestFile[] = drafts.map((d) => ({
      path: normalizeManifestPath(d.path),
      sha256: d.sha256,
      md5: d.md5,
      size: d.size,
      sync: d.sync,
      platform: d.platform,
      artifact: { sha256: d.sha256, size: d.size, codec: d.codec },
    }))
    try {
      const res = await publish.mutateAsync({ channelId: channelId!, files, managedDirs: parsedDirs, note })
      toast.success(t('clientVersions.published', '已发布 v{{n}}', { n: (res as { version?: number })?.version ?? '' }))
      leaveToVersions()
    } catch (err) {
      toast.error(errMsg(err, t('clientVersions.publishFailed', '发布版本失败')))
    }
  }

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div className="space-y-1">
        <button
          type="button"
          className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
          onClick={attemptCancel}
        >
          <ChevronLeft className="size-4" />
          {t('clientPublish.backToChannel', '返回频道工作台')}
        </button>
        <h1 className="text-2xl font-bold flex items-center gap-2">
          <Upload className="size-6" /> {t('clientVersions.publish', '发布新版本')}
        </h1>
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t('clientVersions.wizardDesc', '上传客户端文件（自动计算校验和），设置每个文件的路径与同步策略，再发布。本期为未压缩（codec=none）发布。')}
        </p>
        <p className="text-xs text-muted-foreground font-mono">{channelId}</p>
      </div>

      <PublishStepIndicator step={step} />

      <div className="rounded-xl border bg-card/40 p-5 space-y-4">
        {step === 'files' && (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              {t('clientVersions.stepFilesDesc', '选择要发布的客户端文件（mod、配置、资源包等）。上传后自动计算校验和。')}
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <label className="inline-flex items-center gap-2 px-4 py-2 border rounded-md hover:bg-accent cursor-pointer text-sm">
                {uploading ? <Loader2 className="size-4 animate-spin" /> : <Upload className="size-4" />}
                {t('clientVersions.addFiles', '添加文件')}
                <input type="file" multiple className="hidden" onChange={onPickFiles} disabled={uploading} />
              </label>
              <label className="inline-flex items-center gap-2 px-4 py-2 border rounded-md hover:bg-accent cursor-pointer text-sm">
                {uploading ? <Loader2 className="size-4 animate-spin" /> : <FileArchive className="size-4" />}
                {t('clientVersions.addZip', '上传 ZIP 整合包')}
                <input type="file" accept=".zip,application/zip" className="hidden" onChange={onPickFiles} disabled={uploading} />
              </label>
              <span className="text-xs text-muted-foreground">
                {t('clientVersions.filesCount', '{{n}} 个文件', { n: drafts.length })}
              </span>
            </div>
            <p className="text-xs text-muted-foreground">
              {t('clientVersions.zipHint', '上传 .zip 会在浏览器内解包，按包内目录结构自动编排为下方文件树；散文件与 zip 可混合累加。')}
            </p>
            {progress && (
              <p className="text-xs text-muted-foreground inline-flex items-center gap-1.5">
                <Loader2 className="size-3.5 animate-spin" />
                {t('clientVersions.uploadProgress', '上传中 {{done}}/{{total}}', { done: progress.done, total: progress.total })}
              </p>
            )}
            {drafts.length > 0 && (
              <ul className="border rounded-lg divide-y text-sm">
                {drafts.map((d, i) => (
                  <li key={`${d.sha256}-${i}`} className="flex items-center justify-between gap-2 p-2">
                    <span className="flex min-w-0 items-center gap-2">
                      <FileIcon className="size-3.5 shrink-0 text-muted-foreground" />
                      <span className="font-mono text-xs truncate">{d.path}</span>
                    </span>
                    <span className="flex items-center gap-2 shrink-0">
                      <span className="text-xs text-muted-foreground whitespace-nowrap">{formatBytes(d.size)}</span>
                      <button className="text-destructive hover:opacity-70" onClick={() => removeDraft(i)} aria-label={t('common.delete', '删除')}>
                        <Trash2 className="size-4" />
                      </button>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}

        {step === 'configure' && (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              {t('clientVersions.stepConfigureDesc', '为每个文件设置游戏目录内的目标相对路径、同步策略与适用平台。')}
            </p>
            <p className="text-xs text-muted-foreground inline-flex items-center gap-1">
              {t('clientVersions.lockedHint', '文件内容已锁定（内容寻址 sha256 不可变），仅可编排路径/目录、同步策略、适用平台或移除。')}
            </p>
            <ClientFileTree
              files={drafts}
              onPathChange={(i, path) => patchDraft(i, { path })}
              onSyncChange={(i, sync) => patchDraft(i, { sync })}
              onPlatformChange={(i, platform) => patchDraft(i, { platform })}
              onRemove={removeDraft}
            />
          </div>
        )}

        {step === 'meta' && (
          <div className="space-y-4">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientVersions.managedDirs', '托管目录')}
              <input
                className="p-2 border rounded bg-background font-mono text-xs"
                value={managedDirs}
                onChange={(e) => setManagedDirs(e.target.value)}
                placeholder="mods, config, resourcepacks"
              />
              <span className="text-xs text-muted-foreground">
                {t('clientVersions.managedDirsHint', '逗号/换行分隔。仅这些目录内会删除「本地有但清单未列」的文件（减量）。')}
              </span>
            </label>

            <label className="flex flex-col gap-1 text-sm">
              {t('clientVersions.note', '备注')}
              <input
                className="p-2 border rounded bg-background"
                value={note}
                onChange={(e) => setNote(e.target.value)}
                placeholder={t('clientVersions.notePlaceholder', '如：更新 mods 至 1.20.4')}
              />
            </label>
          </div>
        )}

        {step === 'review' && (
          <div className="space-y-4 text-sm">
            <p className="text-muted-foreground">
              {t('clientVersions.stepReviewDesc', '确认下列内容无误后发布。发布即以更高版本号切为 latest，玩家侧将拉取此版本。')}
            </p>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
              <dt className="text-muted-foreground">{t('clientVersions.fileCount', '文件数')}</dt>
              <dd>{drafts.length}</dd>
              <dt className="text-muted-foreground">{t('clientVersions.managedDirs', '托管目录')}</dt>
              <dd className="font-mono text-xs">{parsedDirs.join(', ') || '-'}</dd>
              <dt className="text-muted-foreground">{t('clientVersions.note', '备注')}</dt>
              <dd>{note || t('clientVersions.noNote', '（无备注）')}</dd>
            </dl>
            <div className="flex items-center justify-end">
              <ReviewViewToggle view={reviewView} onChange={setReviewView} />
            </div>
            {reviewView === 'structure' ? (
              <ClientFileTree files={drafts} readonly />
            ) : (
              <div className="space-y-2">
                <p className="text-xs text-muted-foreground">{t('clientVersions.previewHint')}</p>
                <FileBrowser source={previewSource} className="h-[460px]" />
              </div>
            )}
          </div>
        )}
      </div>

      <div className="flex items-center justify-between gap-2">
        <Button variant="outline" onClick={() => (step === 'files' ? attemptCancel() : setStep(prevStep(step)))}>
          {step === 'files' ? (
            t('common.cancel', '取消')
          ) : (
            <>
              <ArrowLeft className="size-4" /> {t('clientVersions.prevStep', '上一步')}
            </>
          )}
        </Button>
        {step === 'review' ? (
          <Button disabled={!publishable} onClick={doPublish}>
            {publish.isPending && <Loader2 className="size-4 animate-spin" />}
            {t('clientVersions.publish', '发布新版本')}
          </Button>
        ) : (
          <Button disabled={!canAdvance(step, wizardState)} onClick={() => setStep(nextStep(step))}>
            {t('clientVersions.nextStep', '下一步')} <ArrowRight className="size-4" />
          </Button>
        )}
      </div>

      {/* 离开守卫确认：浏览器后退被拦（navBlocked）或点取消（manualDiscard）时弹。 */}
      <DangerConfirm
        open={navBlocked || manualDiscard}
        title={t('clientPublish.discardTitle', '放弃发布草稿？')}
        description={t('clientPublish.discardDesc', '已上传 {{n}} 个文件的编排草稿（文件已在服务端，但「发哪些 + 各自路径/策略」尚未发布）。离开将丢弃这些编排，需重新设置。', { n: drafts.length })}
        confirmLabel={t('clientPublish.discardConfirm', '放弃并离开')}
        onConfirm={() => {
          // 取消(manualDiscard) 或后退被拦(navBlocked)，确认后均解除守卫回工作台。
          setManualDiscard(false)
          setNavBlocked(false)
          leaveToVersions()
        }}
        onCancel={() => {
          if (manualDiscard) setManualDiscard(false)
          else setNavBlocked(false)
        }}
      />
    </div>
  )
}

/** 预览步骤的结构 / 内容预览视图切换（分段按钮，FR-214）。 */
function ReviewViewToggle({ view, onChange }: { view: 'structure' | 'preview'; onChange: (v: 'structure' | 'preview') => void }) {
  const { t } = useTranslation()
  return (
    <div className="inline-flex rounded-lg border p-0.5 text-xs">
      <button
        type="button"
        className={cn('rounded-md px-2.5 py-1 transition-colors', view === 'structure' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground')}
        onClick={() => onChange('structure')}
      >
        {t('clientVersions.viewStructure', '结构')}
      </button>
      <button
        type="button"
        className={cn('rounded-md px-2.5 py-1 transition-colors', view === 'preview' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground')}
        onClick={() => onChange('preview')}
      >
        {t('clientVersions.viewPreview', '预览')}
      </button>
    </div>
  )
}

/** 发布向导步骤指示器（顶部固定、纯展示）。 */
function PublishStepIndicator({ step }: { step: PublishStepId }) {
  const { t } = useTranslation()
  const activeIndex = PUBLISH_STEPS.indexOf(step)
  return (
    <ol className="flex items-center gap-1.5 flex-wrap text-xs">
      {PUBLISH_STEPS.map((s, i) => {
        const meta = PUBLISH_STEP_META[s]
        const done = i < activeIndex
        const active = i === activeIndex
        return (
          <li key={s} className="flex items-center gap-1.5">
            <span
              className={cn(
                'flex items-center gap-1.5 rounded-full px-2.5 py-1',
                active && 'bg-primary/10 text-primary font-medium',
                done && 'text-muted-foreground',
                !active && !done && 'text-muted-foreground/60',
              )}
            >
              <span
                className={cn(
                  'grid size-4 shrink-0 place-items-center rounded-full text-[10px]',
                  active ? 'bg-primary text-primary-foreground' : done ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-500' : 'bg-muted',
                )}
              >
                {done ? <Check className="size-2.5" /> : i + 1}
              </span>
              {t(meta.key, meta.fallback)}
            </span>
            {i < PUBLISH_STEPS.length - 1 && <ArrowRight className="size-3 text-muted-foreground/40" />}
          </li>
        )
      })}
    </ol>
  )
}
