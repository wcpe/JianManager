import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  ClientPublishPageView,
  type ClientPublishOutcome,
  type ClientPublishSubmitPayload,
  type ClientPublishUploadProgress,
} from '@/components/views/client-dist/ClientPublishPageView'
import { usePublishClientVersion, type ManifestFile } from '@/api/clientVersions'
import { useUpdaterJarsInfo } from '@/api/clientChannels'
import { useThemeStore } from '@/stores/theme'
import { uploadFilesEfficient } from '@/lib/efficientUpload'
import {
  PUBLISH_STEPS,
  batchProgressBytes,
  dedupUnits,
  localDedupKey,
  normalizeManifestPath,
  type PublishStepId,
} from '@/lib/client-publish-wizard'

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

/**
 * 客户端分发「发布新版本」独立页面（FR-191，编排重做 FR-250）的接线层（ADR-097）。
 *
 * 展示层已归包（`ClientPublishPageView`），此处只保留应用侧职责：
 * - 步骤与 `?step=` 的双向同步（浏览器前进/后退）；
 * - 取数（内嵌更新器信息）与主题注入（本地预览用），导航（离页回频道工作台版本 tab、缺 channelId 兜底）；
 * - **批量上传编排**：点「发布」才上传——视图把草稿 + 清理范围 + 备注 + 取消信号交来，
 *   这里本地去重（同 name+size 只传一次）→ `uploadFilesEfficient`（算 hash → 秒传预查命中免传 →
 *   miss 小文件聚合、大文件分块，并发 4，进度单调）→ 按草稿顺序回填 `ManifestFile[]` → 提交版本；
 * 任一文件失败：保留全部草稿（视图侧）、停批、可重试（弹错、不清草稿）；成功/取消/失败的提示在此发。
 */
export default function ClientPublishPage() {
  const { t } = useTranslation()
  const { id: channelId } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const publish = usePublishClientVersion()
  const [searchParams, setSearchParams] = useSearchParams()
  const { data: updaterInfo } = useUpdaterJarsInfo()
  const previewTheme = useThemeStore((s) => s.resolvedTheme)

  // 步骤同步 URL ?step=xxx，支持浏览器前进/后退（鼠标侧键）。
  const urlStep = searchParams.get('step') as PublishStepId | null
  const [step, setStepState] = useState<PublishStepId>(
    urlStep && PUBLISH_STEPS.includes(urlStep) ? urlStep : 'files',
  )
  const setStep = useCallback((s: PublishStepId) => {
    setStepState(s)
    setSearchParams(prev => { prev.set('step', s); return prev }, { replace: true })
  }, [setSearchParams])
  // 浏览器前进/后退时同步 step。
  useEffect(() => {
    /* eslint-disable react-hooks/set-state-in-effect */
    const s = searchParams.get('step') as PublishStepId | null
    if (s && PUBLISH_STEPS.includes(s) && s !== step) setStepState(s)
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [searchParams, step])

  // 发布批量上传中（点发布后置位，禁止前进/再次发布；与选文件解耦——选文件不再上传）。
  const [uploading, setUploading] = useState(false)
  // 发布批量上传进度（FR-250/251）：跨所有待上传文件的字节级累计 + 当前文件名/序号。
  const [progress, setProgress] = useState<ClientPublishUploadProgress | null>(null)

  /** 离页：回频道工作台版本 tab（发布成功 / 视图确认放弃草稿）。 */
  const leave = useCallback(() => {
    navigate(`/client-channels?channel=${encodeURIComponent(channelId ?? '')}&tab=versions`)
  }, [navigate, channelId])

  /** 缺失 channelId（异常直链）兜底：回频道列表。 */
  useEffect(() => {
    if (!channelId) navigate('/client-channels', { replace: true })
  }, [channelId, navigate])

  /**
   * 发布：本地去重 → 批量上传（FR-250，FR-346 增效）→ 回填 manifest → 提交版本。
   * 取消信号由视图创建（点发布时）并经载荷透传，取消即中止上传 + 弃单。
   * 结果一律以 `{ ok }` 落定（不抛出）：视图据此决定保留草稿还是离页。
   */
  const doPublish = async (payload: ClientPublishSubmitPayload): Promise<ClientPublishOutcome> => {
    setUploading(true)
    setProgress(null)
    try {
      // 本地去重：同 name+size 仅上传一次；键映射用于为每个草稿（含被去重者）回填复用结果。
      const plan = dedupUnits(payload.files, (d) => ({ name: d.filename, size: d.size }))
      // 键 → 上传结果（sha256/md5/size/codec），供发布时为每个草稿（含被去重者）回填。
      const resultByKey = await uploadFilesEfficient(
        channelId!,
        plan.unique.map((d) => ({ key: localDedupKey(d.filename, d.size), file: d.file, label: d.path })),
        {
          signal: payload.signal,
          onProgress: (p) =>
            setProgress({
              phase: p.phase,
              uploadedBytes: batchProgressBytes(0, p.uploadedBytes, p.totalBytes),
              totalBytes: p.totalBytes,
              currentName:
                p.current?.kind === 'batch'
                  ? t('clientVersions.batchGroupLabel', '聚合小文件 ×{{n}}', { n: p.current.count })
                  : p.current?.name ?? '',
              completedFiles: p.completedFiles,
              fileCount: p.totalFiles,
              hashedFiles: p.hashedFiles,
              totalFilesToHash: p.totalFilesToHash,
              reusedFiles: p.reusedFiles,
            }),
        },
      )

      // 按草稿顺序回填上传结果（codec=none：file 原始内容元数据 = artifact 元数据）。
      const files: ManifestFile[] = payload.files.map((d) => {
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
      const pubRes = await publish.mutateAsync({
        channelId: channelId!,
        files,
        managedDirs: payload.managedDirs,
        cleanExclude: payload.cleanExclude,
        note: payload.note,
      })
      toast.success(t('clientVersions.published', '已发布 v{{n}}', { n: (pubRes as { version?: number })?.version ?? '' }))
      return { ok: true }
    } catch (err) {
      // 用户取消（AbortError）：静默提示、保草稿；其余失败：弹错、保全部草稿可重试（不回退成功的、断点续批）。
      if (isAbortError(err)) {
        toast.info(t('clientVersions.uploadCanceled', '已取消上传'))
      } else {
        toast.error(errMsg(err, t('clientVersions.publishFailed', '发布版本失败')))
      }
      return { ok: false }
    } finally {
      setUploading(false)
      setProgress(null)
    }
  }

  return (
    <ClientPublishPageView
      channelId={channelId}
      step={step}
      onStepChange={setStep}
      // 上传在途或发布请求在途都视为「发布中」（发布请求只发生在上传窗口内）。
      publishing={uploading || publish.isPending}
      progress={progress}
      updaterInfo={updaterInfo}
      previewTheme={previewTheme === 'dark' ? 'dark' : 'light'}
      onPublish={doPublish}
      onLeave={leave}
      onNotify={(kind, message) =>
        kind === 'success' ? toast.success(message) : kind === 'info' ? toast.info(message) : toast.error(message)
      }
    />
  )
}
