import { useCallback, useEffect, useMemo, useState, type ChangeEvent, type DragEvent } from 'react'
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
  FolderUp,
  X,
} from 'lucide-react'
import { usePublishClientVersion, type ManifestFile } from '@/api/clientVersions'
import { uploadFileChunked } from '@/lib/chunkedUpload'
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
  collectEntries,
  dedupUnits,
  localDedupKey,
  batchProgressBytes,
  type PublishStepId,
  type LocalUnit,
  type FileSystemEntryLike,
} from '@/lib/client-publish-wizard'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import DangerConfirm from '@/components/DangerConfirm'
import ClientFileTree from '@/components/ClientFileTree'
import FileBrowser from '@/components/file-browser/FileBrowser'
import { localDraftSource } from '@/components/file-browser/sources/localDraftSource'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/** 判定是否为「用户取消」错误（AbortController.abort() 抛的 AbortError / CanceledError）。 */
function isAbortError(e: unknown): boolean {
  if (e instanceof DOMException && e.name === 'AbortError') return true
  // axios 取消抛 CanceledError（code ERR_CANCELED）。
  const code = (e as { code?: string })?.code
  const name = (e as { name?: string })?.name
  return code === 'ERR_CANCELED' || name === 'CanceledError' || name === 'AbortError'
}

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/** 生成草稿稳定本地 id（React key / 编排定位；仅前端用，不入 manifest）。 */
let draftSeq = 0
function nextDraftId(): string {
  draftSeq += 1
  return `d${draftSeq}`
}

/** 发布批量上传进度（FR-250/251）：字节级累计 + 当前文件与文件序号，供百分比进度条。 */
interface UploadProgress {
  /** 已上传字节（含已完成文件 + 当前文件已传部分）。 */
  uploadedBytes: number
  /** 本批次待上传文件总字节。 */
  totalBytes: number
  /** 当前正在上传的文件名（展示用）。 */
  currentName: string
  /** 当前文件序号（1 基）。 */
  currentIndex: number
  /** 本批次待上传文件总数。 */
  fileCount: number
}

/**
 * 发布向导草稿文件项（FR-250 本地态）。
 * 持浏览器内 `File` 引用 + 本地元数据（path/sync/platform/size）——**尚未上传，无 sha256/md5**；
 * 上传结果在点「发布」时才产生（临时映射，不入草稿态）。
 */
interface DraftFile {
  /** 前端稳定 id（React key / 编排定位）。 */
  id: string
  filename: string
  path: string
  sync: ManifestFile['sync']
  platform: ManifestFile['platform']
  size: number
  /** 浏览器内文件对象（内容源；上传时 slice 流式读，不预载全量进内存）。 */
  file: File
}

/** 向导步骤的标题 i18n 键（顺序与 PUBLISH_STEPS 对齐）。 */
const PUBLISH_STEP_META: Record<PublishStepId, { key: string; fallback: string }> = {
  files: { key: 'clientVersions.stepFiles', fallback: '选择文件' },
  configure: { key: 'clientVersions.stepConfigure', fallback: '逐文件配置' },
  meta: { key: 'clientVersions.stepMeta', fallback: '托管目录 / 说明' },
  review: { key: 'clientVersions.stepReview', fallback: '预览发布' },
}

/**
 * 客户端分发「发布新版本」独立页面（FR-191，编排重做 FR-250）。
 *
 * FR-191 把发布做成独立路由页 `/client-channels/:id/publish`（消除模态误关丢草稿）。
 * FR-250 反转上传时机：**选文件/拖拽不再即刻上传**，文件以浏览器内 `File` 本地暂存，文件树/路径/
 * sync/platform 编排、删除全在本地零网络；点「发布」才**批量分块上传**（复用 FR-251
 * `uploadFileChunked`，带总体进度 + 取消 + 失败保草稿可重试）→ 得各 sha256/md5/size → 提交版本。
 * 省带宽（未发布/发布前删除的文件从不上传）、支持文件夹拖拽保结构。上传 codec=none（不压缩）。
 */
export default function ClientPublishPage() {
  const { t } = useTranslation()
  const { id: channelId } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const publish = usePublishClientVersion()

  const [step, setStep] = useState<PublishStepId>('files')
  const [drafts, setDrafts] = useState<DraftFile[]>([])
  const [managedDirs, setManagedDirs] = useState('mods, config, resourcepacks')
  const [note, setNote] = useState('')
  // 发布批量上传中（点发布后置位，禁止前进/再次发布；与选文件解耦——选文件不再上传）。
  const [uploading, setUploading] = useState(false)
  // 发布批量上传进度（FR-250/251）：跨所有待上传文件的字节级累计 + 当前文件名/序号。
  const [progress, setProgress] = useState<UploadProgress | null>(null)
  // 取消当前批次上传：取消后 uploadFileChunked 抛 AbortError 并向服务端弃单（DELETE）。
  const [uploadAbort, setUploadAbort] = useState<AbortController | null>(null)
  // 已确认离开（发布成功 / 取消确认 / 通过 blocker 确认）——置位即解除离开守卫。
  const [leaving, setLeaving] = useState(false)
  // 点「取消」时若有草稿，开此确认弹窗（后退守卫只拦浏览器后退，取消是页内显式动作需另起确认）。
  const [manualDiscard, setManualDiscard] = useState(false)
  // 拖拽落区高亮（dragover 时置位）。
  const [dragActive, setDragActive] = useState(false)

  // 有本地草稿即「有未发布草稿」——离开页需二次确认（FR-191 离开守卫，FR-250 语义不变）。确认离开后解除。
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

  /**
   * 解包 zip（fflate 客户端解包）为本地单元，path 取自 zip 内相对路径（POSIX 归一）。
   * 跳过目录项与 __MACOSX 噪音；不上传——仅产出本地 File，随后入草稿。
   */
  const unzipToUnits = (data: Uint8Array): Promise<LocalUnit[]> =>
    new Promise((resolve, reject) => {
      unzip(data, (err, unzipped: Unzipped) => {
        if (err) return reject(err)
        const out: LocalUnit[] = []
        for (const [name, bytes] of Object.entries(unzipped)) {
          if (name.endsWith('/')) continue // 目录项
          if (name.startsWith('__MACOSX/') || name.endsWith('.DS_Store')) continue
          const path = normalizeManifestPath(name)
          if (path === '') continue
          const base = path.split('/').pop() || 'file'
          // copy 进独立 ArrayBuffer，避免 File 持有可变视图。
          out.push({ file: new File([bytes.slice().buffer], base), path })
        }
        resolve(out)
      })
    })

  /** 把本地单元累加进草稿（不上传）。sync 默认 strict、platform 默认全平台。 */
  const appendUnits = useCallback((units: LocalUnit[]) => {
    if (units.length === 0) return
    setDrafts((prev) => [
      ...prev,
      ...units.map((u) => ({
        id: nextDraftId(),
        filename: u.file.name,
        path: normalizeManifestPath(u.path),
        sync: 'strict' as ManifestFile['sync'],
        platform: '' as ManifestFile['platform'],
        size: u.file.size,
        file: u.file,
      })),
    ])
  }, [])

  /**
   * 处理一批 File（来自「添加文件」「上传 ZIP」「选择文件夹」选择器或拖拽散文件）：
   * zip 前端解包为多单元，其余按文件名（或 webkitRelativePath 目录）作单元，**均不上传**、累加进草稿。
   */
  const ingestFiles = useCallback(
    async (picked: File[]) => {
      const units: LocalUnit[] = []
      for (const f of picked) {
        if (isZipFilename(f.name)) {
          const buf = new Uint8Array(await f.arrayBuffer())
          units.push(...(await unzipToUnits(buf)))
        } else {
          // webkitdirectory 选择器下 File.webkitRelativePath 携带相对目录；散文件回退文件名。
          const rel = (f as File & { webkitRelativePath?: string }).webkitRelativePath
          units.push({ file: f, path: normalizeManifestPath(rel && rel !== '' ? rel : f.name) })
        }
      }
      appendUnits(units)
    },
    [appendUnits],
  )

  const onPickFiles = async (e: ChangeEvent<HTMLInputElement>) => {
    const picked = Array.from(e.target.files ?? [])
    e.target.value = '' // 允许重复选择同名文件再次触发 change
    if (picked.length === 0) return
    try {
      await ingestFiles(picked)
    } catch (err) {
      toast.error(errMsg(err, t('clientVersions.unzipFailed', '解包 ZIP 失败')))
    }
  }

  /**
   * 拖拽落区 drop：优先用 `webkitGetAsEntry()` 递归遍历（支持**文件夹**保相对路径），
   * 不支持 entry 的浏览器回退 `dataTransfer.files`（仅散文件）。均不上传、累加进草稿。
   */
  const onDrop = async (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    setDragActive(false)
    if (uploading) return
    const items = e.dataTransfer.items
    const entries: FileSystemEntryLike[] = []
    if (items && items.length > 0) {
      for (const item of Array.from(items)) {
        // webkitGetAsEntry 非标准但主流浏览器（Chromium/Firefox/Safari）支持；文件夹拖拽必经此。
        const entry = (item as DataTransferItem & { webkitGetAsEntry?: () => FileSystemEntryLike | null }).webkitGetAsEntry?.()
        if (entry) entries.push(entry)
      }
    }
    try {
      if (entries.length > 0) {
        const units = await collectEntries(entries)
        // zip 仍需前端解包：collectEntries 已得 File，逐一过 zip 判定。
        const expanded: LocalUnit[] = []
        for (const u of units) {
          if (isZipFilename(u.file.name)) {
            const buf = new Uint8Array(await u.file.arrayBuffer())
            expanded.push(...(await unzipToUnits(buf)))
          } else {
            expanded.push(u)
          }
        }
        appendUnits(expanded)
      } else {
        // 回退：无 entry 支持时仅取散文件。
        await ingestFiles(Array.from(e.dataTransfer.files ?? []))
      }
    } catch (err) {
      toast.error(errMsg(err, t('clientVersions.dropFailed', '读取拖入内容失败')))
    }
  }

  const onDragOver = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    if (!uploading) setDragActive(true)
  }
  const onDragLeave = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    setDragActive(false)
  }

  /** 取消当前批次上传：abort 信号触发 uploadFileChunked 中止 + 弃单。 */
  const cancelUpload = () => uploadAbort?.abort()

  const patchDraft = (id: string, patch: Partial<DraftFile>) =>
    setDrafts((prev) => prev.map((d) => (d.id === id ? { ...d, ...patch } : d)))

  const removeDraft = (id: string) => setDrafts((prev) => prev.filter((d) => d.id !== id))

  const wizardState = { draftCount: drafts.length, paths: drafts.map((d) => d.path), uploading }
  const parsedDirs = parseManagedDirs(managedDirs)
  const publishable = canPublish(wizardState) && !publish.isPending

  // 预览（FR-250）：内容预览从**本地 File** 直接读文本（未上传无 sha256），零网络。
  const previewSource = useMemo(
    () => localDraftSource(drafts.map((d) => ({ path: d.path, file: d.file }))),
    [drafts],
  )
  // 预览步骤的视图（结构 = ClientFileTree 编排预览；预览 = 共享 FileBrowser 看本地内容）。
  const [reviewView, setReviewView] = useState<'structure' | 'preview'>('structure')

  /** 尝试取消：有草稿弹二次确认，无草稿直接回工作台。 */
  const attemptCancel = () => {
    if (dirty) setManualDiscard(true)
    else leaveToVersions()
  }

  // ClientFileTree 按源数组 index 回调（编排定位）；这里映射 index→稳定 id 再 patch/remove
  // （drafts 与传入 files 同序，index 即当前 drafts 下标；越界防御返回空串使 patch/remove 空转）。
  const idOf = (index: number) => drafts[index]?.id ?? ''

  /**
   * 发布：点「发布」才批量上传（FR-250）。建 AbortController → 本地去重（同 name+size 只传一次）→
   * 逐个 `uploadFileChunked` 得 sha256/md5/size（归并总体进度）→ 按草稿顺序回填结果组
   * `ManifestFile[]` → 提交版本。任一文件失败：保留全部草稿、停批、可重试（弹错、不清草稿）。
   */
  const doPublish = async () => {
    if (!publishable || uploading) return
    const abort = new AbortController()
    setUploadAbort(abort)
    setUploading(true)
    setProgress(null)
    try {
      // 本地去重：同 name+size 仅上传一次；keys[i] 记第 i 个草稿的去重键，用于回填复用结果。
      const plan = dedupUnits(drafts, (d) => ({ name: d.filename, size: d.size }))
      const totalBytes = plan.unique.reduce((s, d) => s + d.size, 0)
      // 键 → 上传结果（sha256/md5/size/codec），供发布时为每个草稿（含被去重者）回填。
      const resultByKey = new Map<string, { sha256: string; md5: string; size: number; codec: string }>()
      let baseBytes = 0 // 已完成文件累计字节。
      for (let i = 0; i < plan.unique.length; i++) {
        const d = plan.unique[i]
        setProgress({
          uploadedBytes: baseBytes,
          totalBytes,
          currentName: d.path,
          currentIndex: i + 1,
          fileCount: plan.unique.length,
        })
        const res = await uploadFileChunked(channelId!, d.file, {
          signal: abort.signal,
          onProgress: (fileUploaded) =>
            setProgress({
              uploadedBytes: batchProgressBytes(baseBytes, fileUploaded, totalBytes),
              totalBytes,
              currentName: d.path,
              currentIndex: i + 1,
              fileCount: plan.unique.length,
            }),
        })
        resultByKey.set(localDedupKey(d.filename, d.size), res)
        baseBytes += d.size
      }

      // 按草稿顺序回填上传结果（codec=none：file 原始内容元数据 = artifact 元数据）。
      const files: ManifestFile[] = drafts.map((d) => {
        const res = resultByKey.get(localDedupKey(d.filename, d.size))!
        return {
          path: normalizeManifestPath(d.path),
          sha256: res.sha256,
          md5: res.md5,
          size: res.size,
          sync: d.sync,
          platform: d.platform,
          artifact: { sha256: res.sha256, size: res.size, codec: res.codec },
        }
      })
      const pubRes = await publish.mutateAsync({ channelId: channelId!, files, managedDirs: parsedDirs, note })
      toast.success(t('clientVersions.published', '已发布 v{{n}}', { n: (pubRes as { version?: number })?.version ?? '' }))
      leaveToVersions()
    } catch (err) {
      // 用户取消（AbortError）：静默提示、保草稿；其余失败：弹错、保全部草稿可重试（不回退成功的、断点续批）。
      if (isAbortError(err)) {
        toast.info(t('clientVersions.uploadCanceled', '已取消上传'))
      } else {
        toast.error(errMsg(err, t('clientVersions.publishFailed', '发布版本失败')))
      }
    } finally {
      setUploading(false)
      setProgress(null)
      setUploadAbort(null)
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
          {t('clientVersions.wizardDesc', '拖入文件/文件夹本地暂存并编排（此时不上传），点「发布」才批量上传并发布。本期为未压缩（codec=none）发布。')}
        </p>
        <p className="text-xs text-muted-foreground font-mono">{channelId}</p>
      </div>

      <PublishStepIndicator step={step} />

      <div className="rounded-xl border bg-card/40 p-5 space-y-4">
        {step === 'files' && (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              {t('clientVersions.stepFilesDesc', '选择或拖入要发布的客户端文件/文件夹（mod、配置、资源包等）。文件先在浏览器本地暂存，点「发布」才上传。')}
            </p>
            <div
              onDrop={onDrop}
              onDragOver={onDragOver}
              onDragLeave={onDragLeave}
              className={cn(
                'flex flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed p-6 text-center transition-colors',
                dragActive ? 'border-primary bg-primary/5' : 'border-muted-foreground/25',
              )}
              data-testid="publish-dropzone"
            >
              <FolderUp className="size-7 text-muted-foreground" />
              <p className="text-sm font-medium">{t('clientVersions.dropHint', '拖拽文件或文件夹到此处')}</p>
              <p className="text-xs text-muted-foreground">
                {t('clientVersions.dropSubHint', '支持散文件、整个文件夹（保留目录结构）与 ZIP 整合包；均本地暂存、点发布才上传')}
              </p>
              <div className="mt-1 flex flex-wrap items-center justify-center gap-2">
                <label className="inline-flex items-center gap-2 px-4 py-2 border rounded-md hover:bg-accent cursor-pointer text-sm bg-background">
                  <Upload className="size-4" />
                  {t('clientVersions.addFiles', '添加文件')}
                  <input type="file" multiple className="hidden" onChange={onPickFiles} disabled={uploading} />
                </label>
                <label className="inline-flex items-center gap-2 px-4 py-2 border rounded-md hover:bg-accent cursor-pointer text-sm bg-background">
                  <FolderUp className="size-4" />
                  {t('clientVersions.addFolder', '选择文件夹')}
                  {/* webkitdirectory：目录选择器，File.webkitRelativePath 携带相对目录（作等价兜底入口）。 */}
                  <input
                    type="file"
                    multiple
                    className="hidden"
                    onChange={onPickFiles}
                    disabled={uploading}
                    // @ts-expect-error 非标准属性，浏览器支持目录选择
                    webkitdirectory=""
                    directory=""
                  />
                </label>
                <label className="inline-flex items-center gap-2 px-4 py-2 border rounded-md hover:bg-accent cursor-pointer text-sm bg-background">
                  <FileArchive className="size-4" />
                  {t('clientVersions.addZip', '上传 ZIP 整合包')}
                  <input type="file" accept=".zip,application/zip" className="hidden" onChange={onPickFiles} disabled={uploading} />
                </label>
              </div>
            </div>
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span>{t('clientVersions.filesCount', '{{n}} 个文件', { n: drafts.length })}</span>
            </div>
            <p className="text-xs text-muted-foreground">
              {t('clientVersions.zipHint', '上传 .zip 会在浏览器内解包，按包内目录结构自动编排为下方文件树；散文件、文件夹与 zip 可混合累加。')}
            </p>
            {progress && <UploadProgressBar progress={progress} onCancel={cancelUpload} />}
            {drafts.length > 0 && (
              <ul className="border rounded-lg divide-y text-sm">
                {drafts.map((d) => (
                  <li key={d.id} className="flex items-center justify-between gap-2 p-2">
                    <span className="flex min-w-0 items-center gap-2">
                      <FileIcon className="size-3.5 shrink-0 text-muted-foreground" />
                      <span className="font-mono text-xs truncate">{d.path}</span>
                    </span>
                    <span className="flex items-center gap-2 shrink-0">
                      <span className="text-xs text-muted-foreground whitespace-nowrap">{formatBytes(d.size)}</span>
                      <button
                        className="text-destructive hover:opacity-70 disabled:opacity-40"
                        onClick={() => removeDraft(d.id)}
                        disabled={uploading}
                        aria-label={t('common.delete', '删除')}
                      >
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
            <ClientFileTree
              files={drafts}
              onPathChange={(i, path) => patchDraft(idOf(i), { path })}
              onSyncChange={(i, sync) => patchDraft(idOf(i), { sync })}
              onPlatformChange={(i, platform) => patchDraft(idOf(i), { platform })}
              onRemove={(i) => removeDraft(idOf(i))}
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
                <p className="text-xs text-muted-foreground">{t('clientVersions.previewLocalHint', '点左侧文件预览本地内容（文本/配置/JSON 高亮；二进制或过大文件仅可下载）。发布前从本地读取，尚未上传。')}</p>
                <FileBrowser source={previewSource} className="h-[460px]" />
              </div>
            )}
            {progress && <UploadProgressBar progress={progress} onCancel={cancelUpload} />}
          </div>
        )}
      </div>

      <div className="flex items-center justify-between gap-2">
        <Button variant="outline" disabled={uploading} onClick={() => (step === 'files' ? attemptCancel() : setStep(prevStep(step)))}>
          {step === 'files' ? (
            t('common.cancel', '取消')
          ) : (
            <>
              <ArrowLeft className="size-4" /> {t('clientVersions.prevStep', '上一步')}
            </>
          )}
        </Button>
        {step === 'review' ? (
          <Button disabled={!publishable || uploading} onClick={doPublish}>
            {(publish.isPending || uploading) && <Loader2 className="size-4 animate-spin" />}
            {uploading ? t('clientVersions.publishing', '上传并发布中…') : t('clientVersions.publish', '发布新版本')}
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
        description={t('clientPublish.discardDescLocal', '已在本地暂存 {{n}} 个文件的编排草稿（尚未上传）。离开将丢弃这些文件与编排，需重新添加。', { n: drafts.length })}
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

/** 批量上传进度条（FR-250/251）：字节级百分比 + 当前文件/序号 + 取消按钮。 */
function UploadProgressBar({ progress, onCancel }: { progress: UploadProgress; onCancel: () => void }) {
  const { t } = useTranslation()
  const pct = progress.totalBytes > 0 ? Math.round((progress.uploadedBytes / progress.totalBytes) * 100) : 0
  return (
    <div className="space-y-1.5 rounded-lg border bg-background/60 p-3">
      <div className="flex items-center justify-between gap-2 text-xs">
        <span className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
          <Loader2 className="size-3.5 shrink-0 animate-spin" />
          <span className="truncate font-mono" title={progress.currentName}>
            {progress.currentName}
          </span>
        </span>
        <span className="flex shrink-0 items-center gap-2">
          <span className="tabular-nums text-muted-foreground">
            {t('clientVersions.uploadProgressBytes', '{{current}}/{{count}} · {{done}} / {{total}}', {
              current: progress.currentIndex,
              count: progress.fileCount,
              done: formatBytes(progress.uploadedBytes),
              total: formatBytes(progress.totalBytes),
            })}
          </span>
          <span className="tabular-nums font-medium text-foreground">{pct}%</span>
          <button
            type="button"
            className="inline-flex items-center gap-0.5 rounded px-1 py-0.5 text-destructive hover:bg-destructive/10"
            onClick={onCancel}
          >
            <X className="size-3.5" />
            {t('common.cancel', '取消')}
          </button>
        </span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
        <div className="h-full rounded-full bg-primary transition-all duration-200" style={{ width: `${pct}%` }} />
      </div>
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
