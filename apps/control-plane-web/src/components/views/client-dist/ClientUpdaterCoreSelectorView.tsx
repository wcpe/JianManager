/**
 * @file ClientUpdaterCoreSelectorView：频道工作台「Core 版本」Tab 的 updater-core 版本选择器受控视图，
 *       版本列表由容器取数注入，切换/上传写请求与 toast 由容器负责。
 * @input views/DangerConfirm（切换二次确认；allowed 由容器读角色注入）、Badge/Button/Table/Dialog 原语、
 *        lucide 图标、翻译上下文
 * @output ClientUpdaterCoreSelectorView（+ ClientUpdaterCoreSelectorViewProps、ClientUpdaterCoreVersion、
 *         ClientUpdaterCoreUploadPayload、ClientUpdaterCoreUploadOutcome）
 * @sync apps/control-plane-web/src/components/ClientUpdaterCoreSelector.tsx（容器）、
 *       apps/control-plane-web/src/components/ClientUpdaterCoreSelector.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-259 updater-core 归档版本切换与 hotfix 上传）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, History, Upload } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import DangerConfirm from '@/components/views/common/DangerConfirm'

/**
 * updater-core 归档版本摘要（容器经 `useUpdaterCoreVersions` 取数注入）。
 * 刻意只声明本视图读到的字段（`coreVersion`/`buildTime` 等展示层不用），与应用侧 API 类型同形结构互通。
 */
export interface ClientUpdaterCoreVersion {
  /** 数字归档版本，兼容旧接口与 wedge 分发兜底。 */
  version: number
  /** 推荐展示版本：coreVersion + gitCommit，dirty 时带 .dirty。 */
  displayVersion?: string
  /** 构建 updater-core.jar 时的短提交 hash。 */
  gitCommit?: string
  /** 构建时是否存在未提交的已跟踪文件变更。 */
  dirty?: boolean
  /** 内容 sha256（版本唯一键，也是切换动作的定位键）。 */
  sha256: string
  /** jar 体积（字节）。 */
  size: number
  /** 归档时间（RFC3339）。 */
  createdAt: string
  /** 是否为该频道当前选定版本（后端据频道 SelectedCoreSHA256 标记）。 */
  selected: boolean
}

/** 手动上传载荷（表单草稿由本视图持有，写请求归容器）。 */
export interface ClientUpdaterCoreUploadPayload {
  /** 待上传的 jar 文件（浏览器内对象）。 */
  file: File
  /** 版本号原文（可空：后端优先读 jar 内版本）。 */
  version: string
  /** 上传后是否立即选为当前频道版本。 */
  select: boolean
}

/**
 * 上传结果回执：`ok=true` 表示已上传成功（请求与成功/失败 toast 均在容器），视图据此清空表单并关窗；
 * `ok=false`（含异常，容器已提示）时视图保留表单与弹窗，可原样重试。
 */
export interface ClientUpdaterCoreUploadOutcome {
  ok: boolean
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast、不读路由**。
 * - 版本列表与加载态：`versions` / `loading` 由容器取数注入（`versions` 缺省/空数组渲染空态）；
 * - 切换：行内「选定」只把目标 sha256 记进本视图的确认弹窗（弹窗开合属 UI 状态），确认后经 `onSelect`
 *   上报（PUT 请求与成功/失败 toast 在容器）；`selecting` 为在途禁用态，避免重复提交；
 * - 上传：弹窗开合与表单草稿（file / version / select）留本视图，提交经 `onUpload` 交容器（POST + toast），
 *   仅在回执 `ok=true` 时清空表单并关窗；
 * - `dangerAllowed` 由容器读登录态角色注入——**切换确认的 scope/allowed 语义与迁包前逐字一致**
 *   （`scope="platform"`、`allowed` 由容器注入；组件库不持鉴权状态，缺省视为放行）。
 */
export interface ClientUpdaterCoreSelectorViewProps {
  /** 归档版本列表（容器取数注入；缺省或空数组渲染空态行）。 */
  versions?: ClientUpdaterCoreVersion[]
  /** 列表加载态（渲染「加载中…」行）。 */
  loading: boolean
  /** 切换在途（容器注入 mutation pending）：禁用各行「选定」按钮。 */
  selecting?: boolean
  /** 确认切换（容器执行 PUT + toast）；参数为目标准确 sha256。 */
  onSelect: (sha256: string) => void
  /** 上传在途（容器注入 mutation pending）：驱动弹窗按钮禁用与「上传中…」文案。 */
  uploading?: boolean
  /** 提交上传（容器执行 POST + toast）；回执决定视图是否清空并关窗。 */
  onUpload: (payload: ClientUpdaterCoreUploadPayload) => Promise<ClientUpdaterCoreUploadOutcome>
  /** 危险操作（切换版本）是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  dangerAllowed?: boolean
}

/**
 * updater-core 版本选择器（FR-259）。频道工作台「Core 版本」tab：
 * 列出所有归档 core 版本，当前选定版本高亮，一键切换实现回滚。
 * 切换后提示"客户端下次启动生效"。
 */
export function ClientUpdaterCoreSelectorView({
  versions,
  loading,
  selecting = false,
  onSelect,
  uploading = false,
  onUpload,
  dangerAllowed,
}: ClientUpdaterCoreSelectorViewProps) {
  const { t } = useTranslation()
  // 待确认的切换目标 sha256（null = 未开确认弹窗）：弹窗开合属 UI 状态，留本视图。
  const [target, setTarget] = useState<string | null>(null)
  // 上传弹窗开合属 UI 状态，留本视图。
  const [uploadOpen, setUploadOpen] = useState(false)

  const selectedVersion = versions?.find((v) => v.selected)
  const latestVersion = versions?.[0]

  /** 确认切换：先记下目标并关窗，写请求与 toast 交给容器。 */
  const doSelect = () => {
    if (!target) return
    const sha = target
    setTarget(null)
    onSelect(sha)
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t(
            'clientCore.subtitle',
            '列出所有归档的 updater-core 版本。切换选定版本后，客户端下次启动按 endpoint 自动查询并使用该版本——用于坏 core 应急回滚。',
          )}
        </p>
        <Button variant="outline" onClick={() => setUploadOpen(true)}>
          <Upload className="size-4" /> {t('clientCore.upload', '上传 updater-core.jar')}
        </Button>
      </div>

      <div className="grid gap-2 sm:grid-cols-2">
        <div className="rounded-lg border bg-card p-3">
          <div className="text-[11px] text-muted-foreground">{t('clientCore.latestArchived', '最新归档版本')}</div>
          <div className="mt-1 font-mono text-lg font-semibold">
            {latestVersion ? displayCoreVersion(latestVersion) : '—'}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground">
            {latestVersion ? `${latestVersion.sha256.slice(0, 12)}… · ${formatBytes(latestVersion.size)}` : t('clientCore.noVersionsShort', '暂无归档')}
          </div>
        </div>
        <div className="rounded-lg border bg-card p-3">
          <div className="text-[11px] text-muted-foreground">{t('clientCore.currentSelected', '当前选定版本')}</div>
          <div className="mt-1 font-mono text-lg font-semibold">
            {selectedVersion ? displayCoreVersion(selectedVersion) : latestVersion ? t('clientCore.followLatest', '跟随最新') : '—'}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground">
            {selectedVersion
              ? `${selectedVersion.sha256.slice(0, 12)}… · ${formatBytes(selectedVersion.size)}`
              : t('clientCore.followLatestHint', '频道未固定版本时，客户端使用最新归档 updater-core。')}
          </div>
        </div>
      </div>

      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('clientCore.colVersion', '版本')}</TableHead>
              <TableHead>{t('clientCore.colSha256', 'SHA256')}</TableHead>
              <TableHead>{t('clientCore.colSize', '大小')}</TableHead>
              <TableHead>{t('clientCore.colCreatedAt', '归档时间')}</TableHead>
              <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {versions?.map((v) => (
              <TableRow key={v.sha256} className={v.selected ? 'bg-primary/5' : undefined}>
                <TableCell className="font-medium">
                  <span className="flex items-center gap-2">
                    <History className="size-3.5 text-muted-foreground" />
                    {displayCoreVersion(v)}
                  </span>
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  <div>{v.sha256.slice(0, 12)}…</div>
                  <div className="mt-1 flex items-center gap-1 text-[11px]">
                    <span>{v.gitCommit ? `${v.gitCommit.slice(0, 12)}${v.dirty ? '.dirty' : ''}` : '—'}</span>
                    {v.dirty ? <Badge variant="outline">dirty</Badge> : null}
                  </div>
                </TableCell>
                <TableCell className="text-xs">{formatBytes(v.size)}</TableCell>
                <TableCell className="text-xs">{new Date(v.createdAt).toLocaleString()}</TableCell>
                <TableCell>
                  <div className="flex justify-end items-center gap-2">
                    {v.selected ? (
                      <Badge variant="default" className="gap-1">
                        <Check className="size-3" /> {t('clientCore.selected', '当前选定')}
                      </Badge>
                    ) : (
                      <Button
                        variant="outline"
                        size="xs"
                        disabled={selecting}
                        onClick={() => setTarget(v.sha256)}
                      >
                        {t('clientCore.select', '选定')}
                      </Button>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            ))}
            {versions?.length === 0 && !loading && (
              <TableRow>
                <TableCell colSpan={5} className="h-16 text-center text-muted-foreground">
                  {t('clientCore.noVersions', '暂无归档版本（需 make embed-client-updater 构建后启动 CP 归档）')}
                </TableCell>
              </TableRow>
            )}
            {loading && (
              <TableRow>
                <TableCell colSpan={5} className="h-16 text-center text-muted-foreground">
                  {t('common.loading', '加载中…')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <UploadCoreDialog open={uploadOpen} onOpenChange={setUploadOpen} onUpload={onUpload} uploading={uploading} />

      <DangerConfirm
        open={target !== null}
        title={t('clientCore.switchConfirm', '切换 updater-core 版本？')}
        description={t(
          'clientCore.switchDesc',
          '切换后客户端下次启动按 endpoint 自动查询并使用该版本。本地已有该版本 jar 的客户端直接用、没有的自动下载。请确认确需切换。',
        )}
        scope="platform"
        allowed={dangerAllowed}
        confirmLabel={t('clientCore.select', '选定')}
        onConfirm={doSelect}
        onCancel={() => setTarget(null)}
      />
    </div>
  )
}

/**
 * 上传 updater-core 弹窗：开合与表单草稿（file / version / select）留本视图，
 * 上传请求与 toast 由容器承担——回执 `ok=true` 才清空表单并关窗，失败（容器已提示）保留表单可原样重试。
 */
function UploadCoreDialog({
  open,
  onOpenChange,
  onUpload,
  uploading = false,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onUpload: (payload: ClientUpdaterCoreUploadPayload) => Promise<ClientUpdaterCoreUploadOutcome>
  uploading?: boolean
}) {
  const { t } = useTranslation()
  const [file, setFile] = useState<File | null>(null)
  const [version, setVersion] = useState('')
  const [select, setSelect] = useState(true)

  const reset = () => {
    setFile(null)
    setVersion('')
    setSelect(true)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!file || uploading) return
    const res = await onUpload({ file, version, select })
    if (!res.ok) return
    reset()
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('clientCore.uploadTitle', '上传 updater-core.jar')}</DialogTitle>
          <DialogDescription>
            {t(
              'clientCore.uploadDesc',
              '用于紧急 hotfix：上传后会归档为 client-updater-core 制品，可选择立即作为当前频道版本。',
            )}
          </DialogDescription>
        </DialogHeader>
        <form id="upload-core-form" className="space-y-4" onSubmit={submit}>
          <label className="flex flex-col gap-1 text-sm">
            {t('clientCore.jarFile', 'Jar 文件')}
            <input
              type="file"
              accept=".jar,application/java-archive"
              className="p-2 border rounded bg-background"
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            {t('clientCore.version', '版本号')}
            <input
              className="p-2 border rounded bg-background"
              placeholder="9"
              value={version}
              onChange={(e) => setVersion(e.target.value)}
            />
            <span className="text-xs text-muted-foreground">
              {t('clientCore.versionHint', '通常无需填写，后端优先读取 jar 内版本；紧急 hotfix 缺少元信息也可上传。')}
            </span>
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={select} onChange={(e) => setSelect(e.target.checked)} />
            {t('clientCore.selectAfterUpload', '上传后立即选为当前频道版本')}
          </label>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="upload-core-form" disabled={!file || uploading}>
            {uploading ? t('common.uploading', '上传中…') : t('clientCore.upload', '上传 updater-core.jar')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 展示版本：优先 jar 内声明的 displayVersion，缺失时回退数字归档版本。 */
function displayCoreVersion(v: { version: number; displayVersion?: string }): string {
  return v.displayVersion || `v${v.version}`
}

/** formatBytes 把字节数格式化为人类可读（KB/MB）。 */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
