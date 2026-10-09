/**
 * @file ClientPublishPageView：客户端分发「发布新版本」向导页（步骤指示 → 选文件 → 逐文件配置 →
 *       清理规则/说明 → 预览发布）的受控视图。上传编排（本地去重 → hash → 秒传预查 → 分块/聚合上传）、
 *       版本发布请求、toast 与路由跳转由应用容器负责；视图只保留本地草稿、文件摄入（zip 解包 /
 *       目录 entry 拖拽 / 右键定点上传）、步骤与预览展示、放弃草稿守卫，并全部经 onXxx 上报。
 * @input lib/client-publish-wizard（PUBLISH_STEPS/canAdvance/canPublish/nextStep/prevStep/normalizeManifestPath/
 *        isZipFilename/hasPublishDraft/collectEntries/joinDirPath/CLEAN_ALL_SENTINEL/isCleanAll 与
 *        PublishStepId/LocalUnit/FileSystemEntryLike/ManifestFileLike 类型；纯逻辑，零网络）、
 *        lib/zip-filename-decode（unzipWithNames 浏览器内解包）、lib/webkit-entry-adapter（adaptEntry 与原生 entry 类型）、
 *        lib/file-sources（localDraftSource：本地草稿零网络数据源）、views/explorer/FileExplorer（草稿编排树）、
 *        views/client-dist/CleanScopeEditor（清理范围树）、views/client-dist/EmbeddedUpdaterParts（内嵌更新器摘要）、
 *        views/file-browser/FileBrowser（本地内容预览）、views/DangerConfirm、Button/Checkbox/layout 原语、
 *        lucide 图标、翻译上下文
 * @output ClientPublishPageView（+ ClientPublishPageViewProps）、ClientPublishDraftFile、
 *         ClientPublishUploadProgress、ClientPublishSubmitPayload、ClientPublishOutcome、
 *         UploadProgressBar（+ Props）、ReviewViewToggle（+ Props）、PublishStepIndicator（+ Props）
 * @sync apps/control-plane-web/src/pages/ClientPublishPage.tsx、
 *       apps/control-plane-web/src/pages/ClientPublishPage.dom.test.tsx
 * @since FR-502（页面受控化迁包；原 FR-191 独立路由页、FR-250 本地暂存 + 延迟批量上传、FR-255 清理范围、FR-350 定点上传）
 */
import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent, type DragEvent } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Upload,
  Loader2,
  ArrowLeft,
  ArrowRight,
  Check,
  FileArchive,
  FolderUp,
  X,
} from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import FileExplorer from '@/components/views/explorer/FileExplorer'
import FileBrowser from '@/components/views/file-browser/FileBrowser'
import { CleanScopeEditor } from '@/components/views/client-dist/CleanScopeEditor'
import { EmbeddedUpdaterSummary } from '@/components/views/client-dist/EmbeddedUpdaterParts'
import type { EmbeddedUpdaterSummaryProps } from '@/components/views/client-dist/EmbeddedUpdaterParts'
import { localDraftSource } from '@/lib/file-browser/file-sources'
import { adaptEntry } from '@/lib/shared/webkit-entry-adapter'
import type { NativeFileSystemEntry } from '@/lib/shared/webkit-entry-adapter'
import { unzipWithNames } from '@/lib/shared/zip-filename-decode'
import { PUBLISH_STEPS, canAdvance, canPublish, nextStep, prevStep, normalizeManifestPath, isZipFilename, hasPublishDraft, collectEntries, joinDirPath, CLEAN_ALL_SENTINEL, isCleanAll } from '@/lib/client-dist/client-publish-wizard'
import type { PublishStepId, LocalUnit, FileSystemEntryLike, ManifestFileLike } from '@/lib/client-dist/client-publish-wizard'
import { formatFileSize } from '@/lib/shared/format-file-size'

/** 取异常里的服务端文案，回退到给定文案（本地解包/拖拽解析失败时的文案口径与迁包前一致）。 */
function errMsg(e: unknown, fallback: string): string {
  const resp = e as { response?: { data?: { message?: string } } }
  return resp?.response?.data?.message || fallback
}

/** 字节数转人类可读。 */
/** 生成草稿稳定本地 id（React key / 编排定位；仅前端用，不入 manifest）。 */
let draftSeq = 0
function nextDraftId(): string {
  draftSeq += 1
  return `d${draftSeq}`
}

/**
 * 发布批量上传进度（FR-250/251，FR-346 增效后）：字节级累计 + 阶段 + 秒传计数。
 * **由容器产出**（它持有上传编排）：视图只做展示，不参与计算。
 */
export interface ClientPublishUploadProgress {
  /** 阶段：hashing=本地校验计算（零网络）；uploading=字节上传（含秒传落定）。 */
  phase: 'hashing' | 'uploading'
  /** 已上传字节（含已完成任务 + 在途部分，单调不倒退）。 */
  uploadedBytes: number
  /** 本批次待上传文件总字节。 */
  totalBytes: number
  /** 当前活跃任务的展示名（并发下取最近启动者；聚合批为 i18n 文案）。 */
  currentName: string
  /** 已落定文件数（含秒传命中）。 */
  completedFiles: number
  /** 本批次待上传文件总数。 */
  fileCount: number
  /** hashing 阶段：已校验 / 需校验文件数。 */
  hashedFiles: number
  totalFilesToHash: number
  /** 秒传命中文件数（内容已在制品库，免字节上传）。 */
  reusedFiles: number
}

/**
 * 发布向导草稿文件项（FR-250 本地态）。
 * 持浏览器内 `File` 引用 + 本地元数据（path/sync/platform/size）——**尚未上传，无 sha256/md5**；
 * 上传结果在点「发布」时才产生（临时映射，不入草稿态）。
 */
export interface ClientPublishDraftFile extends ManifestFileLike {
  /** 前端稳定 id（React key / 编排定位）。 */
  id: string
  filename: string
  /** 浏览器内文件对象（内容源；上传时 slice 流式读，不预载全量进内存）。 */
  file: File
}

/**
 * 发布提交载荷（视图 → 容器）：草稿原文 + 清理范围产出 + 备注 + 取消信号。
 * **请求体组装（manifest 条目、去重键、上传结果回填）属上传编排，留容器**。
 */
export interface ClientPublishSubmitPayload {
  /** 待发布草稿（含浏览器内 File；容器据 filename/size 去重后上传）。 */
  files: ClientPublishDraftFile[]
  /** 最终自动清理目录：clean-all 时为 `["*"]` 哨兵，否则为目录树勾选 + 草稿外自定义目录。 */
  managedDirs: string[]
  /** 自定义追加排除（FR-255）：空数组视为未设置（不下发）。 */
  cleanExclude?: string[]
  /** 版本备注原文。 */
  note: string
  /** 取消信号：视图点「取消上传」时 abort；容器透传给上传编排（取消即弃单）。 */
  signal: AbortSignal
}

/**
 * 发布结果回执：`ok=true` 表示已上传并发布成功（成功/取消/失败 toast 与请求均由容器负责）；
 * `ok=false`（含用户取消）时视图保留全部草稿可重试。
 */
export interface ClientPublishOutcome {
  ok: boolean
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast、不读路由**。
 * - 步骤：`step` 由容器持有并与 `?step=` 双向同步（支持浏览器前进/后退），视图只上报
 *   `onStepChange`（「上一步 / 下一步」两个入口）；
 * - 上传与发布：点「发布」时视图把草稿 + 清理范围 + 备注 + 取消信号经 `onPublish` 交容器
 *   （本地去重、hash、秒传预查、分块/聚合上传、manifest 回填、`POST /versions` 全在容器）；
 *   进度经 `progress` 回流展示；`ok=true` 时视图请求离页，失败（含取消）保留草稿与步骤；
 * - 取消上传：视图自持 `AbortController`（仅在点发布时创建），abort 经载荷 `signal` 生效；
 * - 内嵌更新器摘要：`updaterInfo` 由容器取数注入（缺省不渲染）；
 * - 本地预览主题：`previewTheme` 由容器读主题 store 注入（包内不持有主题状态）；
 * - 轻提示：仅本地解包/拖拽解析失败（不涉及网络层文案）经 `onNotify` 上报，toast 由容器发；
 * - 离页：`onLeave` 由容器导航回频道工作台版本 tab（发布成功 / 用户确认放弃草稿）；
 * - **留本视图**的 UI 状态：草稿（含文件摄入与 zip 解压）、清理范围草稿、备注、预览视图开关、
 *   拖拽高亮、clean-all 二次确认开合、放弃草稿二次确认（含浏览器后退守卫与关页守卫）。
 */
export interface ClientPublishPageViewProps {
  /** 目标频道 id（页头等宽展示；容器取路由参数）。 */
  channelId?: string
  /** 当前步骤（容器持有并与 `?step=` 同步）。 */
  step: PublishStepId
  /** 步骤变更上报（视图的「上一步 / 下一步」入口）。 */
  onStepChange: (step: PublishStepId) => void
  /** 上传 + 发布在途（容器 = 批次上传中或发布请求在途）：禁用前进/发布/选文件并驱动按钮态。 */
  publishing: boolean
  /** 批次上传进度（容器聚合上报）；为 null 不渲染进度条。 */
  progress: ClientPublishUploadProgress | null
  /** 内嵌更新器版本信息（容器调 `useUpdaterJarsInfo()` 注入）；缺省不渲染摘要行。 */
  updaterInfo?: EmbeddedUpdaterSummaryProps['info']
  /** 预览用解析后主题（容器读主题 store 注入）；缺省 light。 */
  previewTheme?: 'light' | 'dark'
  /**
   * 发布（容器做上传编排 + 提交版本 + toast）：返回 `ok=false` 时视图保留草稿可重试，
   * 返回 `ok=true` 时视图调用 `onLeave`。
   */
  onPublish: (payload: ClientPublishSubmitPayload) => Promise<ClientPublishOutcome>
  /** 离页（发布成功 / 确认放弃草稿）：容器导航回频道工作台版本 tab。 */
  onLeave: () => void
  /** 轻提示通道（仅本地解包/拖拽解析失败）：toast 由容器负责。 */
  onNotify?: (kind: 'success' | 'error' | 'info', message: string) => void
}

/** 向导步骤的标题 i18n 键（顺序与 PUBLISH_STEPS 对齐）。 */
const PUBLISH_STEP_META: Record<PublishStepId, { key: string; fallback: string }> = {
  files: { key: 'clientVersions.stepFiles', fallback: '选择文件' },
  configure: { key: 'clientVersions.stepConfigure', fallback: '逐文件配置' },
  meta: { key: 'clientVersions.stepMeta', fallback: '清理规则 / 说明' },
  review: { key: 'clientVersions.stepReview', fallback: '预览发布' },
}

/**
 * 客户端分发「发布新版本」向导页受控视图（FR-191，编排重做 FR-250）。
 *
 * FR-191 把发布做成独立路由页（消除模态误关丢草稿）。FR-250 反转上传时机：**选文件/拖拽不再
 * 即刻上传**，文件以浏览器内 `File` 本地暂存，文件树/路径/sync/platform 编排、删除全在本地零网络；
 * 点「发布」才把草稿交容器**批量分块上传**（带总体进度 + 取消 + 失败保草稿可重试）→ 得各
 * sha256/md5/size → 提交版本。省带宽（未发布/发布前删除的文件从不上传）、支持文件夹拖拽保结构。
 * 上传 codec=none（不压缩）。
 */
export function ClientPublishPageView({
  channelId,
  step,
  onStepChange,
  publishing,
  progress,
  updaterInfo,
  previewTheme = 'light',
  onPublish,
  onLeave,
  onNotify,
}: ClientPublishPageViewProps) {
  const { t } = useTranslation()

  const [drafts, setDrafts] = useState<ClientPublishDraftFile[]>([])
  // FR-255：managedDirs 改为目录树勾选（selectedDirs）+ 高级手动兜底（managedDirsManual）。
  const [selectedDirs, setSelectedDirs] = useState<string[]>([])
  // 草稿外自定义目录（如 mods、custom-mods），显示在清理目录树中可标记。
  const [extraDirs, setExtraDirs] = useState<string[]>([])
  // 「清空整个 gameDir」开关（clean-all）：开启后 managedDirs 含哨兵 "*"，删清单未列的一切。
  const [cleanAll, setCleanAll] = useState(false)
  // 自定义追加排除（FR-255）：命中前缀的路径永不删（叠加在玩家区之上）。
  const [cleanExclude, setCleanExclude] = useState<string[]>([])
  // clean-all 发布二次确认弹窗（开启时点发布先弹 DangerConfirm）。
  const [cleanAllConfirm, setCleanAllConfirm] = useState(false)
  const [note, setNote] = useState('')
  // 点「取消」时若有草稿，开此确认弹窗（后退守卫只拦浏览器后退，取消是页内显式动作需另起确认）。
  const [manualDiscard, setManualDiscard] = useState(false)
  // 拖拽落区高亮（dragover 时置位）。
  const [dragActive, setDragActive] = useState(false)
  // 预览步骤的视图（结构 = FileExplorer 编排预览；预览 = 共享 FileBrowser 看本地内容）。
  const [reviewView, setReviewView] = useState<'structure' | 'preview'>('structure')
  // 当前批次上传的取消信号（点发布时创建；容器经载荷 signal 使用，abort 即中止 + 弃单）。
  const uploadAbort = useRef<AbortController | null>(null)

  // 有本地草稿即「有未发布草稿」——离开页需二次确认（FR-191 离开守卫，FR-250 语义不变）。
  const dirty = hasPublishDraft(drafts.length)

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

  /**
   * 解包 zip（客户端自解析解包）为本地单元，path 取自 zip 内相对路径（POSIX 归一）。
   * 文件名按 zip UTF-8 标志位选 UTF-8/GBK 解码（FR-250/BUG-G：中文 Windows zip 常存 GBK）。
   * 跳过目录项与 __MACOSX 噪音；不上传——仅产出本地 File，随后入草稿。
   */
  const unzipToUnits = async (data: Uint8Array): Promise<LocalUnit[]> => {
    const entries = await unzipWithNames(data)
    const out: LocalUnit[] = []
    for (const { name, data: bytes } of entries) {
      if (name.startsWith('__MACOSX/') || name.endsWith('.DS_Store')) continue
      const path = normalizeManifestPath(name)
      if (path === '') continue
      const base = path.split('/').pop() || 'file'
      // copy 进独立 ArrayBuffer，避免 File 持有可变视图。
      out.push({ file: new File([bytes.slice().buffer], base), path })
    }
    return out
  }

  /** 把本地单元累加进草稿（不上传）。sync 默认 strict、platform 默认全平台。 */
  const appendUnits = useCallback((units: LocalUnit[]) => {
    if (units.length === 0) return
    setDrafts((prev) => [
      ...prev,
      ...units.map((u) => ({
        id: nextDraftId(),
        filename: u.file.name,
        path: normalizeManifestPath(u.path),
        sync: 'strict' as ManifestFileLike['sync'],
        platform: '' as ManifestFileLike['platform'],
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

  /**
   * FR-350 右键定点上传：把 FileExplorer 交来的「所选文件 + 目标目录」摄入草稿。
   * zip 解包后逐条拼目录前缀；文件夹选择（webkitdirectory）保留 webkitRelativePath
   * 内部相对结构拼在前缀后；散文件回退文件名。均本地暂存、不上传。
   */
  const ingestFilesToDir = useCallback(
    async (picked: File[], targetDirPath: string) => {
      const units: LocalUnit[] = []
      for (const f of picked) {
        if (isZipFilename(f.name)) {
          const buf = new Uint8Array(await f.arrayBuffer())
          for (const u of await unzipToUnits(buf)) {
            units.push({ ...u, path: joinDirPath(targetDirPath, u.path) })
          }
        } else {
          const rel = (f as File & { webkitRelativePath?: string }).webkitRelativePath
          const sub = normalizeManifestPath(rel && rel !== '' ? rel : f.name)
          units.push({ file: f, path: joinDirPath(targetDirPath, sub) })
        }
      }
      appendUnits(units)
    },
    [appendUnits],
  )

  /** FileExplorer onUploadToDir 回调（同步签名）：异步摄入 + 解包失败弹错。 */
  const onUploadToDir = useCallback(
    (picked: File[], targetDirPath: string) => {
      ingestFilesToDir(picked, targetDirPath).catch((err) => {
        onNotify?.('error', errMsg(err, t('clientVersions.unzipFailed', '解包 ZIP 失败')))
      })
    },
    [ingestFilesToDir, onNotify, t],
  )

  const onPickFiles = async (e: ChangeEvent<HTMLInputElement>) => {
    const picked = Array.from(e.target.files ?? [])
    e.target.value = '' // 允许重复选择同名文件再次触发 change
    if (picked.length === 0) return
    try {
      await ingestFiles(picked)
    } catch (err) {
      onNotify?.('error', errMsg(err, t('clientVersions.unzipFailed', '解包 ZIP 失败')))
    }
  }

  /**
   * 拖拽落区 drop：优先用 `webkitGetAsEntry()` 递归遍历（支持**文件夹**保相对路径），
   * 不支持 entry 的浏览器回退 `dataTransfer.files`（仅散文件）。均不上传、累加进草稿。
   */
  const onDrop = async (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    setDragActive(false)
    if (publishing) return
    const items = e.dataTransfer.items
    const entries: FileSystemEntryLike[] = []
    if (items && items.length > 0) {
      for (const item of Array.from(items)) {
        // webkitGetAsEntry 非标准但主流浏览器（Chromium/Firefox/Safari）支持；文件夹拖拽必经此。
        // 原生 entry 是回调式（file(cb)/createReader().readEntries(cb)），须经 adaptEntry
        // Promise 化后才能喂 collectEntries（FR-250/BUG-F）——否则文件取不到、目录被跳过、拖拽失效。
        const native = (item as DataTransferItem & { webkitGetAsEntry?: () => NativeFileSystemEntry | null }).webkitGetAsEntry?.()
        if (native) entries.push(adaptEntry(native))
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
      onNotify?.('error', errMsg(err, t('clientVersions.dropFailed', '读取拖入内容失败')))
    }
  }

  const onDragOver = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    if (!publishing) setDragActive(true)
  }
  const onDragLeave = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    setDragActive(false)
  }

  /** FileExplorer 拖入文件解析：散文件 + zip 解压，加 targetDirPath 前缀。 */
  const resolveDrop = async (dropped: File[], targetDirPath: string): Promise<LocalUnit[]> => {
    const out: LocalUnit[] = []
    for (const f of dropped) {
      if (isZipFilename(f.name)) {
        const buf = new Uint8Array(await f.arrayBuffer())
        const units = await unzipToUnits(buf)
        for (const u of units) {
          out.push({ ...u, path: targetDirPath ? `${targetDirPath}/${u.path}` : u.path })
        }
      } else {
        out.push({ file: f, path: targetDirPath ? `${targetDirPath}/${f.name}` : f.name })
      }
    }
    return out
  }

  /** 取消当前批次上传：abort 信号触发上传编排中止 + 弃单。 */
  const cancelUpload = () => uploadAbort.current?.abort()

  const patchDraft = (id: string, patch: Partial<ClientPublishDraftFile>) =>
    setDrafts((prev) => prev.map((d) => (d.id === id ? { ...d, ...patch } : d)))

  const removeDraft = (id: string) => setDrafts((prev) => prev.filter((d) => d.id !== id))

  const wizardState = { draftCount: drafts.length, paths: drafts.map((d) => d.path), uploading: publishing }
  const publishable = canPublish(wizardState)
  // FR-255：最终 managedDirs——clean-all 时为 ["*"] 哨兵；否则为目录树勾选（含草稿外自定义目录）。
  const effectiveManagedDirs = cleanAll
    ? [CLEAN_ALL_SENTINEL]
    : Array.from(new Set([...selectedDirs, ...extraDirs.filter(d => !cleanExclude.includes(d))]))
  const effectiveCleanExclude = cleanExclude.length > 0 ? cleanExclude : undefined
  // 预览数据源：从本地草稿 File 读内容（零网络，FR-250）。
  const previewSource = useMemo(() => localDraftSource(drafts.map(d => ({ path: d.path, file: d.file }))), [drafts])

  /** 尝试取消：有草稿弹二次确认，无草稿直接回工作台。 */
  const attemptCancel = () => {
    if (dirty) setManualDiscard(true)
    else onLeave()
  }

  // ClientFileTree 按源数组 index 回调（编排定位）；这里映射 index→稳定 id 再 patch/remove
  // （drafts 与传入 files 同序，index 即当前 drafts 下标；越界防御返回空串使 patch/remove 空转）。
  const idOf = (index: number) => drafts[index]?.id ?? ''

  /**
   * 发布：把草稿交容器批量上传（FR-250，FR-346 增效）——容器建去重计划、编排上传、按草稿顺序
   * 回填 manifest 并提交版本。此处只负责开取消信号、转发载荷、据结果决定去留。
   */
  const doPublish = async () => {
    if (!publishable || publishing) return
    const abort = new AbortController()
    uploadAbort.current = abort
    try {
      const res = await onPublish({
        files: drafts,
        managedDirs: effectiveManagedDirs,
        cleanExclude: effectiveCleanExclude,
        note,
        signal: abort.signal,
      })
      // 成功：容器已提示并解除守卫需求，请求离页；失败（含取消）：保留全部草稿可重试。
      if (res.ok) onLeave()
    } finally {
      uploadAbort.current = null
    }
  }

  /**
   * 触发发布（FR-255）：开启 clean-all 时先弹 DangerConfirm 二次确认，明示删除范围后才能继续；
   * 普通模式直接发布。
   */
  const attemptPublish = () => {
    if (isCleanAll(effectiveManagedDirs)) {
      setCleanAllConfirm(true)
    } else {
      void doPublish()
    }
  }

  return (
    // 全量对齐：外壳与页头改用布局层原语，与创建实例向导同处理——它是窄栏流程页，
    // 保留 mx-auto max-w-4xl；返回语义由面包屑承载，原先左侧的返回按钮去掉
    // （与向导页同理：它与页名并列时，读屏会先念按钮再念标题）。
    // channelId 并入 description（等宽），不单独占一行。
    <PageShell className="mx-auto max-w-4xl" data-page="client-publish">
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <Upload className="size-6" />
            {t('clientVersions.publish', '发布新版本')}
          </span>
        }
        description={
          <>
            {t('clientVersions.wizardDesc', '拖入文件/文件夹本地暂存并编排（此时不上传），点「发布」才批量上传并发布。本期为未压缩（codec=none）发布。')}
            {' · '}
            <span className="font-mono">{channelId}</span>
          </>
        }
        breadcrumbs={[
          { label: t('nav.clientChannels'), to: '/client-channels' },
          { label: t('clientVersions.publish', '发布新版本') },
        ]}
      />
      <EmbeddedUpdaterSummary info={updaterInfo} />

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
                  <input type="file" multiple className="hidden" onChange={onPickFiles} disabled={publishing} />
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
                    disabled={publishing}
                    // @ts-expect-error 非标准属性，浏览器支持目录选择
                    webkitdirectory=""
                    directory=""
                  />
                </label>
                <label className="inline-flex items-center gap-2 px-4 py-2 border rounded-md hover:bg-accent cursor-pointer text-sm bg-background">
                  <FileArchive className="size-4" />
                  {t('clientVersions.addZip', '上传 ZIP 整合包')}
                  <input type="file" accept=".zip,application/zip" className="hidden" onChange={onPickFiles} disabled={publishing} />
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
              <FileExplorer
                files={drafts}
                onPathChange={(i, path) => patchDraft(idOf(i), { path })}
                onRemove={(i) => removeDraft(idOf(i))}
                onRemoveMultiple={(indices) => indices.forEach((i) => removeDraft(idOf(i)))}
                resolveDrop={resolveDrop}
                onAddFiles={(units) => appendUnits(units)}
                onUploadToDir={onUploadToDir}
              />
            )}
          </div>
        )}

        {step === 'configure' && (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              {t('clientVersions.stepConfigureDesc', '为每个文件设置它在玩家游戏目录里的存放路径、同步策略（覆盖 / 仅一次 / 忽略）与适用平台。')}
            </p>
            <p className="rounded-md bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
              {t('clientVersions.syncModeHint', '同步策略 = 玩家更新时怎么处理这个文件：覆盖（强制统一）· 仅一次（缺了才补）· 忽略（完全不管）')}
            </p>
            <p className="text-xs text-muted-foreground">
              {t('clientVersions.dragArrangeHint', '拖拽文件或目录节点到其他目录可批量改目标路径')}
            </p>
            <FileExplorer
              files={drafts}
              onPathChange={(i, path) => patchDraft(idOf(i), { path })}
              onSyncChange={(i, sync) => patchDraft(idOf(i), { sync })}
              onPlatformChange={(i, platform) => patchDraft(idOf(i), { platform })}
              onRemove={(i) => removeDraft(idOf(i))}
              onRemoveMultiple={(indices) => indices.forEach((i) => removeDraft(idOf(i)))}
              resolveDrop={resolveDrop}
              onAddFiles={(units) => appendUnits(units)}
              onUploadToDir={onUploadToDir}
            />
          </div>
        )}

        {step === 'meta' && (
          <div className="space-y-4">
            {/* 「清空整个 gameDir」开关（FR-255）：开启后 managedDirs=["*"]，删清单未列的一切。 */}
            <label className="flex items-start gap-2.5 rounded-lg border p-3 text-sm">
              <Checkbox
                checked={cleanAll}
                onCheckedChange={(v) => setCleanAll(v === true)}
                className="mt-0.5"
                data-testid="clean-all-toggle"
              />
              <span className="flex flex-col gap-1">
                <span className="font-medium">{t('clientVersions.cleanAllGameDir', '清空整个游戏目录')}</span>
                <span className="text-xs text-muted-foreground">
                  {t('clientVersions.cleanAllHint', '开启后，玩家更新时会删除游戏目录内清单未列的一切文件，仅保留存档/截图/日志等安全区与下方自定义排除项。危险：玩家在游戏目录自放的非保护文件也会被删，发布前需二次确认。')}
                </span>
              </span>
            </label>

            {/* 目录树标记（clean-all 接管时禁用） */}
            <div className="space-y-1.5">
              <span className="text-sm font-medium">
                {t('clientVersions.managedDirsTreeTitle', '自动清理目录')}
              </span>
              <CleanScopeEditor
                files={drafts}
                managedDirs={selectedDirs}
                cleanExclude={cleanExclude}
                onChange={(md, ce) => {
                  setSelectedDirs(md)
                  setCleanExclude(ce)
                }}
                cleanAll={cleanAll}
                extraDirs={extraDirs}
                onExtraDirsChange={setExtraDirs}
              />
              <span className="text-xs text-muted-foreground">
                {t('clientVersions.cleanScopeTreeHint', '右键目录标记为清理（红）或排除（绿），Ctrl+点击追加选、Shift+点击连选、右键批量标记。父标记后子目录继承；子目录单独改标记后父目录变为混合色（橙）。')}
              </span>
            </div>

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
              {t('clientVersions.stepReviewDesc', '确认无误后点发布。发布后这个版本会成为玩家自动下载的最新版（版本号只增不减，玩家不会被降级）。')}
            </p>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
              <dt className="text-muted-foreground">{t('clientVersions.fileCount', '文件数')}</dt>
              <dd>{drafts.length}</dd>
              <dt className="text-muted-foreground">{t('clientVersions.reviewManagedDirs', '自动清理')}</dt>
              <dd className="flex flex-wrap items-center gap-1.5 font-mono text-xs">
                {cleanAll && (
                  <span className="rounded bg-destructive/10 px-1.5 py-0.5 text-destructive" data-testid="review-clean-all-badge">
                    {t('clientVersions.cleanAllBadge', '清空整个游戏目录')}
                  </span>
                )}
                {effectiveManagedDirs.filter((d) => d !== CLEAN_ALL_SENTINEL).join(', ') || (cleanAll ? '' : '-')}
              </dd>
              <dt className="text-muted-foreground">{t('clientVersions.reviewCleanExclude', '永不清理')}</dt>
              <dd className="font-mono text-xs">{effectiveCleanExclude?.join(', ') || '-'}</dd>
              <dt className="text-muted-foreground">{t('clientVersions.note', '备注')}</dt>
              <dd>{note || t('clientVersions.noNote', '（无备注）')}</dd>
            </dl>
            <div className="flex items-center justify-end">
              <ReviewViewToggle view={reviewView} onChange={setReviewView} />
            </div>
            {reviewView === 'structure' ? (
              <FileExplorer files={drafts} readonly />
            ) : (
              <div className="space-y-2">
                <p className="text-xs text-muted-foreground">{t('clientVersions.previewLocalHint', '点左侧文件预览本地内容（文本/配置/JSON 高亮；二进制或过大文件仅可下载）。发布前从本地读取，尚未上传。')}</p>
                <FileBrowser source={previewSource} className="h-[460px]" theme={previewTheme} />
              </div>
            )}
            {progress && <UploadProgressBar progress={progress} onCancel={cancelUpload} />}
          </div>
        )}
      </div>

      <div className="flex items-center justify-between gap-2">
        <Button variant="outline" disabled={publishing} onClick={() => (step === 'files' ? attemptCancel() : onStepChange(prevStep(step)))}>
          {step === 'files' ? (
            t('common.cancel', '取消')
          ) : (
            <>
              <ArrowLeft className="size-4" /> {t('clientVersions.prevStep', '上一步')}
            </>
          )}
        </Button>
        {step === 'review' ? (
          <Button disabled={!publishable || publishing} onClick={attemptPublish}>
            {publishing && <Loader2 className="size-4 animate-spin" />}
            {publishing ? t('clientVersions.publishing', '上传并发布中…') : t('clientVersions.publish', '发布新版本')}
          </Button>
        ) : (
          <Button disabled={!canAdvance(step, wizardState)} onClick={() => onStepChange(nextStep(step))}>
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
          onLeave()
        }}
        onCancel={() => {
          if (manualDiscard) setManualDiscard(false)
          else setNavBlocked(false)
        }}
      />

      {/* FR-255：clean-all 发布二次确认——开启「清空整个 gameDir」时点发布先弹此确认。 */}
      <DangerConfirm
        open={cleanAllConfirm}
        title={t('clientVersions.cleanAllConfirmTitle', '确认清空整个游戏目录？')}
        description={t('clientVersions.cleanAllConfirmDesc', '开启「清空整个游戏目录」后发布，玩家该频道游戏目录内、清单未列且不在保护区与自定义排除内的文件将被删除。此操作不可逆，请确认。')}
        confirmLabel={t('clientVersions.cleanAllConfirmLabel', '我已知晓风险，发布')}
        onConfirm={() => {
          setCleanAllConfirm(false)
          void doPublish()
        }}
        onCancel={() => setCleanAllConfirm(false)}
      />
    </PageShell>
  )
}

/** 批量上传进度条的受控入参：进度由容器产出，取消回调由视图接入当前批次信号。 */
export interface UploadProgressBarProps {
  progress: ClientPublishUploadProgress
  /** 取消当前批次上传（容器中止上传 + 弃单）。 */
  onCancel: () => void
}

/** 批量上传进度条（FR-250/251，FR-346 增效）：hashing/uploading 双阶段 + 秒传计数 + 取消。 */
export function UploadProgressBar({ progress, onCancel }: UploadProgressBarProps) {
  const { t } = useTranslation()
  const hashing = progress.phase === 'hashing'
  // hashing 阶段按文件数、uploading 阶段按字节，分母为 0 时恒 0（不产生 NaN）。
  const pct = hashing
    ? progress.totalFilesToHash > 0
      ? Math.round((progress.hashedFiles / progress.totalFilesToHash) * 100)
      : 0
    : progress.totalBytes > 0
      ? Math.round((progress.uploadedBytes / progress.totalBytes) * 100)
      : 0
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
          {progress.reusedFiles > 0 && (
            <span
              className="rounded-full bg-emerald-500/10 px-1.5 py-0.5 text-emerald-600 dark:text-emerald-500"
              data-testid="upload-reused-chip"
            >
              {t('clientVersions.reusedInstant', '秒传 {{n}} 个', { n: progress.reusedFiles })}
            </span>
          )}
          <span className="tabular-nums text-muted-foreground">
            {hashing
              ? t('clientVersions.hashingProgress', '校验文件 {{done}}/{{total}}', {
                  done: progress.hashedFiles,
                  total: progress.totalFilesToHash,
                })
              : t('clientVersions.uploadProgressBytes', '{{current}}/{{count}} · {{done}} / {{total}}', {
                  current: Math.min(progress.completedFiles + 1, progress.fileCount),
                  count: progress.fileCount,
                  done: formatFileSize(progress.uploadedBytes),
                  total: formatFileSize(progress.totalBytes),
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

/** 预览步骤结构 / 内容视图切换的受控入参（视图开关本身留包内）。 */
export interface ReviewViewToggleProps {
  view: 'structure' | 'preview'
  onChange: (v: 'structure' | 'preview') => void
}

/** 预览步骤的结构 / 内容预览视图切换（分段按钮，FR-214）。 */
export function ReviewViewToggle({ view, onChange }: ReviewViewToggleProps) {
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

/** 发布向导步骤指示器的受控入参（当前步骤由容器与 `?step=` 同步）。 */
export interface PublishStepIndicatorProps {
  step: PublishStepId
}

/** 发布向导步骤指示器（顶部固定、纯展示）。 */
export function PublishStepIndicator({ step }: PublishStepIndicatorProps) {
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
