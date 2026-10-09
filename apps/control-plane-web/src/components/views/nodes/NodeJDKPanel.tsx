import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Download, FolderOpen, Loader2, Package, PackageCheck, Pencil, Coffee, Search, Trash2 } from 'lucide-react'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Badge } from '@jianmanager/ui/components/badge'
import { ViewToggle, type ViewMode } from '@jianmanager/ui/components/view-toggle'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/views/DangerConfirm'
import { copyToClipboard } from '@jianmanager/ui/lib/clipboard'

/** JDK 厂商集（foojay 支持，可自定义其它发行版）。 */
const VENDOR_OPTIONS: ComboboxOption[] = [
  { value: 'Temurin' },
  { value: 'Corretto' },
  { value: 'Zulu' },
  { value: 'Liberica' },
  { value: 'Microsoft' },
  { value: 'Semeru' },
  { value: 'GraalVM' },
]
/** CPU 架构常用集（可自定义）。 */
const ARCH_OPTIONS: ComboboxOption[] = [
  { value: 'x64' },
  { value: 'aarch64' },
]

/** 探测结果只读行（FR-228）：标签 + 值。 */
function ProbeRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4 px-3 py-1.5">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className={cn('min-w-0 break-all text-right', mono && 'font-mono text-xs')}>{value || '—'}</dd>
    </div>
  )
}

/** 一条已登记的 JDK（本组件所需的最小结构；外壳传 API 返回项会结构兼容）。 */
export interface NodeJDKView {
  id: number
  vendor: string
  majorVersion: number
  version: string
  arch: string
  path: string
  /** 平台托管（删除会连文件一起删）。 */
  managed: boolean
}

/** 路径探测结果（同上）。 */
export interface ProbeResultView {
  valid: boolean
  vendor: string
  majorVersion: number
  version: string
  arch: string
  /** 探测出的 JDK 根目录（登记时作为 path 提交）。 */
  javaHome: string
  error?: string
}

/** foojay 版本目录的一个可选项（同上）。 */
export interface JDKCatalogItemView {
  javaVersion: string
  latest?: boolean
  archiveType: string
}

/** 面板内子视图：已登记列表 / 一键下载 / 登记已有（分段切换，容器固定不重排）。 */
type JDKTab = 'list' | 'install' | 'register'

/**
 * 节点 JDK 管理面板（FR-178 重做）：
 * - 表格横向不溢出（overflow-x-auto）、路径可复制、删除走 DangerConfirm；
 * - 一键下载支持多厂商 + foojay 具体版本选择器，下发接任务中心（FR-183）；
 * - 登记已有用目录选择器选路径（非手敲）。
 * 三个动作以分段切换（容器稳定，符合抽屉 UX 约束：不切换隐显内联表单致布局重组）。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast。五类动作回调化，
 * 其中「探测」与「刷新目录」必须**回传结果**（探测结果要展示、要驱动登记表单可用性）；
 * 表单草稿、分段、筛选、编辑草稿等 UI 状态留在组件内。
 *
 * 三处注入点（都是本视图不该认识的取数组件）：
 * - `pingSlot`：安装前的节点存活测试（自带取数）；
 * - `renderDirectoryPicker`：目录选择器（自带取数，且视图要控制其开关并接收选定路径，
 *   故用渲染函数而非普通 slot）；
 * - `runtimeSlot`：分区末尾的运行时库（自成一块、自行取数）。
 */
export interface NodeJDKPanelProps {
  /** 已登记 JDK 列表；外壳取数注入。 */
  jdks?: NodeJDKView[]
  /** 首次加载态。 */
  isLoading?: boolean
  /** 后台同步中（列表从节点回同步时显式提示，避免「卡住无反馈」）。 */
  isFetching?: boolean
  /** foojay 版本目录：加载中。 */
  catalogLoading?: boolean
  /** foojay 目录不可用（出错或无结果）→ 降级为手填具体版本。 */
  catalogUnavailable?: boolean
  /** foojay 目录选项。 */
  catalogVersions?: JDKCatalogItemView[]
  /**
   * 上报 foojay 目录的查询键（厂商 + 大版本 + 是否处于「一键下载」分段）。
   * 这两个值是视图内的表单草稿，却同时是目录查询的键，故变化时上报、由外壳执行查询。
   */
  onCatalogQuery?: (vendor: string, major: number, open: boolean) => void
  /** 一键下载在途。 */
  installing?: boolean
  /** 登记在途。 */
  creating?: boolean
  /** 路径探测在途。 */
  probing?: boolean
  /** 编辑保存在途。 */
  saving?: boolean
  /** 安装前的节点存活测试位（外壳注入，自带取数）。 */
  pingSlot?: ReactNode
  /** 目录选择器渲染器（外壳注入，自带取数）。 */
  renderDirectoryPicker?: (args: { onPick: (path: string) => void; onCancel: () => void }) => ReactNode
  /** 分区末尾的运行时库（外壳注入，自成一块）。 */
  runtimeSlot?: ReactNode
  /** 触发一键下载。返回是否成功（成功则回列表分段）。 */
  onInstall: (body: { vendor: string; majorVersion: number; arch: string; version?: string }) => Promise<boolean>
  /** 登记探测到的 JDK。返回是否成功（成功则清空草稿并回列表分段）。 */
  onRegister: (body: {
    vendor: string
    majorVersion: number
    version: string
    arch: string
    path: string
    managed: boolean
  }) => Promise<boolean>
  /** 探测路径。成功回传结果；失败回 null（视图保留输入以便重试）。 */
  onProbe: (path: string) => Promise<ProbeResultView | null>
  /** 保存编辑（FR-311）。返回是否成功。 */
  onUpdate: (
    jdkId: number,
    body: { vendor: string; majorVersion: number; version: string; arch: string; path: string },
  ) => Promise<boolean>
  /** 删除登记。返回是否成功（失败原因由外壳提示）。 */
  onDelete: (jdk: NodeJDKView) => Promise<boolean>
  /** 复制路径的结果上报（由外壳决定提示文案）。 */
  onCopyResult?: (ok: boolean) => void
}

export default function NodeJDKPanel({
  jdks,
  isLoading,
  isFetching = false,
  catalogLoading = false,
  catalogUnavailable = false,
  catalogVersions = [],
  onCatalogQuery,
  installing = false,
  creating = false,
  probing = false,
  saving = false,
  pingSlot,
  renderDirectoryPicker,
  runtimeSlot,
  onInstall,
  onRegister,
  onProbe,
  onUpdate,
  onDelete,
  onCopyResult,
}: NodeJDKPanelProps) {
  const { t } = useTranslation()

  const [tab, setTab] = useState<JDKTab>('list')

  // 安装表单状态。
  const [vendor, setVendor] = useState('Temurin')
  const [major, setMajor] = useState('21')
  const [version, setVersion] = useState('') // 具体版本（空=该大版本最新）
  const [arch, setArch] = useState('x64')

  // 登记表单状态（FR-228：选目录 + 后端探测自动填，不再手填厂商/版本/架构）。
  const [regManaged, setRegManaged] = useState(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [probed, setProbed] = useState<ProbeResultView | null>(null)
  const [pathInput, setPathInput] = useState('') // 登记路径：可手输或经选目录填入（FR-228 细化）

  const [pendingDel, setPendingDel] = useState<NodeJDKView | null>(null)
  const [view, setView] = useState<ViewMode>('list')
  const [query, setQuery] = useState('')
  const [sourceFilter, setSourceFilter] = useState<'all' | 'managed' | 'external'>('all')

  // 编辑登记信息（FR-311）：行内铅笔 → 模态改厂商/大版本/具体版本/arch/路径，走既有 PUT。
  const [editing, setEditing] = useState<NodeJDKView | null>(null)
  const [editForm, setEditForm] = useState({ vendor: '', majorVersion: '', version: '', arch: '', path: '' })
  const openEdit = (j: NodeJDKView) => {
    setEditing(j)
    setEditForm({ vendor: j.vendor, majorVersion: String(j.majorVersion), version: j.version ?? '', arch: j.arch ?? '', path: j.path })
  }
  const submitEdit = async () => {
    if (!editing) return
    const ok = await onUpdate(editing.id, {
      vendor: editForm.vendor.trim(),
      majorVersion: Number(editForm.majorVersion) || editing.majorVersion,
      version: editForm.version.trim(),
      arch: editForm.arch.trim(),
      path: editForm.path.trim(),
    })
    if (ok) setEditing(null)
  }

  const majorNum = Number(major) || 0

  // 厂商/大版本是 foojay 目录的查询键（换厂商/版本即换查询），且只在「一键下载」
  // 分段才需要——故把三者的组合上报外壳，由它决定何时发起查询。
  // 外壳侧用幂等 setState 吸收重复上报，避免本 effect 触发重渲染循环。
  useEffect(() => {
    onCatalogQuery?.(vendor, majorNum, tab === 'install')
  }, [vendor, majorNum, tab, onCatalogQuery])

  // 已登记列表的来源筛选 + 文本搜索（FR-195：行卡片 / 网格视图的轻量过滤）。
  const allJdks = jdks ?? []
  const managedCount = allJdks.filter((j) => j.managed).length
  const filteredJdks = allJdks.filter((j) => {
    if (sourceFilter === 'managed' && !j.managed) return false
    if (sourceFilter === 'external' && j.managed) return false
    const q = query.trim().toLowerCase()
    if (q && !`${j.vendor} ${j.majorVersion} ${j.version} ${j.arch} ${j.path}`.toLowerCase().includes(q)) return false
    return true
  })

  // 来源徽章：托管=主色「包」图标+文字，外部=中性「文件夹」图标+文字（FR-195 锁定样式）。
  const sourceBadge = (managed: boolean) =>
    managed ? (
      <span className="inline-flex items-center gap-1 whitespace-nowrap text-xs font-medium text-primary">
        <Package className="size-3.5" />
        {t('nodes.jdkManaged')}
      </span>
    ) : (
      <span className="inline-flex items-center gap-1 whitespace-nowrap text-xs text-muted-foreground">
        <FolderOpen className="size-3.5" />
        {t('nodes.jdkExternal')}
      </span>
    )

  const submitInstall = async () => {
    // FR-183：异步任务，回执 taskId；进度/完成在「任务中心」与站内信查看。
    const ok = await onInstall({ vendor, majorVersion: majorNum, arch, version: version.trim() || undefined })
    if (ok) setTab('list')
  }

  const submitRegister = async (e: FormEvent) => {
    e.preventDefault()
    if (!probed?.valid) return
    const ok = await onRegister({
      vendor: probed.vendor,
      majorVersion: probed.majorVersion,
      version: probed.version,
      arch: probed.arch,
      path: probed.javaHome,
      managed: regManaged,
    })
    if (ok) {
      setProbed(null)
      setPathInput('')
      setRegManaged(false)
      setTab('list')
    }
  }

  // 探测路径（FR-228）：后端 java -version 自动得出厂商/版本/架构，结果存 probed。手输或选目录都走这里。
  const runProbe = async (path: string) => {
    if (!path) return
    const res = await onProbe(path)
    // 失败时清空上一次结果，避免把旧结果当成新路径的探测结论。
    setProbed(res)
  }

  // 选目录后填入路径并自动探测（FR-228）。
  const onPickPath = (path: string) => {
    setPickerOpen(false)
    setPathInput(path)
    void runProbe(path)
  }

  const copyPath = async (p: string) => {
    const ok = await copyToClipboard(p)
    onCopyResult?.(ok)
  }

  return (
    <div className="space-y-3">
      {/* 分段切换：固定高度的工具条，切换不致下方内容上下重排 */}
      <div className="flex items-center gap-1 rounded-md border bg-muted/30 p-1 text-sm">
        {(['list', 'install', 'register'] as JDKTab[]).map((k) => (
          <button
            key={k}
            type="button"
            onClick={() => setTab(k)}
            className={`flex-1 rounded px-3 py-1.5 transition-colors ${
              tab === k ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground hover:text-foreground'
            }`}
          >
            {t(`artifactCache.jdkTab.${k}`)}
          </button>
        ))}
      </div>

      {tab === 'list' && (
        <div className="space-y-3">
          {/* 工具条：搜索 + 视图切换（行卡片 ⇄ 网格） */}
          <div className="flex items-center gap-2">
            <div className="relative flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t('nodes.jdkSearchPlaceholder')}
                className="h-9 pl-8"
              />
            </div>
            <ViewToggle value={view} onChange={setView} cardLabel={t('grouping.viewCard')} listLabel={t('grouping.viewList')} />
          </div>

          {/* 同步反馈（FIX-5）：登记后 / 打开面板时列表从节点同步（GET /jdks 的 syncFromWorker）显式提示，不再静默「卡住无反馈」。 */}
          {isFetching && !isLoading && (
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Loader2 className="size-3 animate-spin" />
              {t('nodes.jdkSyncing')}
            </p>
          )}

          {/* 来源筛选 chips */}
          <div className="flex flex-wrap items-center gap-2">
            {([
              ['all', t('grouping.all'), allJdks.length],
              ['managed', t('nodes.jdkManaged'), managedCount],
              ['external', t('nodes.jdkExternal'), allJdks.length - managedCount],
            ] as const).map(([key, label, count]) => (
              <button
                key={key}
                type="button"
                onClick={() => setSourceFilter(key)}
                aria-pressed={sourceFilter === key}
                className={cn(
                  'inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors',
                  sourceFilter === key
                    ? 'border-primary/50 bg-accent text-primary'
                    : 'border-border bg-card text-muted-foreground hover:text-foreground',
                )}
              >
                {label}
                <span className="font-semibold tabular-nums">{count}</span>
              </button>
            ))}
          </div>

          {/* 列表 / 网格 / 空 / 载入 */}
          {isLoading ? (
            /* 骨架占位：按 JDK 行卡片（size-9 图标 + 双行文本 ≈ h-14）轮廓，替代裸文字。 */
            <div className="space-y-2">
              <Skeleton className="h-14 w-full" />
              <Skeleton className="h-14 w-full" />
              <Skeleton className="h-14 w-full" />
            </div>
          ) : filteredJdks.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">{t('nodes.jdkEmpty')}</p>
          ) : view === 'list' ? (
            <div className="space-y-2">
              {filteredJdks.map((j) => (
                <div
                  key={j.id}
                  className="flex items-center gap-3 rounded-lg border bg-card px-3 py-2.5 transition-colors hover:bg-muted/40"
                >
                  <div
                    className={cn(
                      'flex size-9 shrink-0 items-center justify-center rounded-md',
                      j.managed ? 'bg-accent text-primary' : 'bg-muted text-muted-foreground',
                    )}
                  >
                    <Coffee className="size-[18px]" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{j.vendor}</span>
                      <Badge variant="secondary" className="bg-accent font-medium text-primary">
                        Java {j.majorVersion}
                      </Badge>
                      {(j.version || j.arch) && (
                        <span className="text-xs text-muted-foreground">{[j.version, j.arch].filter(Boolean).join(' · ')}</span>
                      )}
                    </div>
                    <button
                      type="button"
                      className="mt-0.5 flex max-w-full items-center gap-1 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
                      title={j.path}
                      onClick={() => void copyPath(j.path)}
                    >
                      <span className="truncate">{j.path}</span>
                      <Copy className="size-3 shrink-0" />
                    </button>
                  </div>
                  {sourceBadge(j.managed)}
                  <button
                    type="button"
                    aria-label={t('nodes.jdkEdit', '编辑')}
                    className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
                    onClick={() => openEdit(j)}
                  >
                    <Pencil className="size-4" />
                  </button>
                  <button
                    type="button"
                    aria-label={t('common.delete')}
                    className="shrink-0 text-muted-foreground transition-colors hover:text-status-danger"
                    onClick={() => setPendingDel(j)}
                  >
                    <Trash2 className="size-4" />
                  </button>
                </div>
              ))}
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              {filteredJdks.map((j) => (
                <div key={j.id} className="rounded-lg border bg-card p-3 transition-colors hover:bg-muted/40">
                  <div className="mb-2.5 flex items-center gap-2.5">
                    <div
                      className={cn(
                        'flex size-8 shrink-0 items-center justify-center rounded-md',
                        j.managed ? 'bg-accent text-primary' : 'bg-muted text-muted-foreground',
                      )}
                    >
                      <Coffee className="size-4" />
                    </div>
                    <span className="flex-1 truncate font-medium">{j.vendor}</span>
                    <Badge variant="secondary" className="bg-accent font-medium text-primary">
                      Java {j.majorVersion}
                    </Badge>
                    <button
                      type="button"
                      aria-label={t('nodes.jdkEdit', '编辑')}
                      className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
                      onClick={() => openEdit(j)}
                    >
                      <Pencil className="size-4" />
                    </button>
                    <button
                      type="button"
                      aria-label={t('common.delete')}
                      className="shrink-0 text-muted-foreground transition-colors hover:text-status-danger"
                      onClick={() => setPendingDel(j)}
                    >
                      <Trash2 className="size-4" />
                    </button>
                  </div>
                  <dl className="space-y-1 text-xs">
                    <div className="flex items-center justify-between">
                      <dt className="text-muted-foreground">{t('nodes.jdkVersion')}</dt>
                      <dd>{j.version || '—'}</dd>
                    </div>
                    <div className="flex items-center justify-between">
                      <dt className="text-muted-foreground">{t('nodes.jdkArch')}</dt>
                      <dd>{j.arch || '—'}</dd>
                    </div>
                    <div className="flex items-center justify-between">
                      <dt className="text-muted-foreground">{t('nodes.jdkSource')}</dt>
                      <dd>{sourceBadge(j.managed)}</dd>
                    </div>
                  </dl>
                  <button
                    type="button"
                    className="mt-2 flex w-full items-center gap-1 border-t pt-2 font-mono text-[11px] text-muted-foreground transition-colors hover:text-foreground"
                    title={j.path}
                    onClick={() => void copyPath(j.path)}
                  >
                    <span className="truncate">{j.path}</span>
                    <Copy className="size-3 shrink-0" />
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {tab === 'install' && (
        <div className="space-y-3 rounded-md border p-3 text-sm">
          <div className="grid grid-cols-2 gap-3">
            <label className="space-y-1">
              <span className="font-medium">{t('nodes.jdkVendor')}</span>
              <Combobox options={VENDOR_OPTIONS} value={vendor} onChange={(v) => { setVendor(v); setVersion('') }} />
            </label>
            <label className="space-y-1">
              <span className="font-medium">{t('nodes.jdkMajor')}</span>
              <Input value={major} onChange={(e) => { setMajor(e.target.value); setVersion('') }} inputMode="numeric" />
            </label>
            <label className="space-y-1">
              <span className="font-medium">{t('nodes.jdkArch')}</span>
              <Combobox options={ARCH_OPTIONS} value={arch} onChange={setArch} />
            </label>
            <label className="space-y-1">
              <span className="font-medium">{t('artifactCache.jdkVersionPick')}</span>
              {catalogLoading ? (
                <p className="px-1 py-1.5 text-xs text-muted-foreground">{t('artifactCache.jdkCatalogLoading')}</p>
              ) : catalogUnavailable ? (
                // foojay 不可达/无结果：降级为手填具体版本（仍可下载）。
                <Input value={version} onChange={(e) => setVersion(e.target.value)} placeholder={t('artifactCache.jdkVersionLatest')} />
              ) : (
                <Combobox
                  options={[
                    { value: '', label: t('artifactCache.jdkVersionLatest') },
                    ...catalogVersions.map((p) => ({
                      value: p.javaVersion,
                      label: `${p.javaVersion}${p.latest ? ` (${t('artifactCache.jdkLatest')})` : ''} · ${p.archiveType}`,
                    })),
                  ]}
                  value={version}
                  onChange={setVersion}
                  allowCustom={false}
                  placeholder={t('artifactCache.jdkVersionLatest')}
                />
              )}
            </label>
          </div>
          {/* 安装预览：明示将装什么、经代理、进度去向，替代干巴巴一行提示 */}
          <div className="flex items-start gap-2 rounded-md bg-primary/5 px-3 py-2.5 text-primary">
            <PackageCheck className="mt-0.5 size-4 shrink-0" />
            <div className="min-w-0 text-sm">
              <p className="font-medium">
                {t('artifactCache.jdkInstallPreview')}: {vendor} {version || `${major} · ${t('artifactCache.jdkLatest')}`} · {arch}
              </p>
              <p className="mt-0.5 text-xs text-primary/70">{t('artifactCache.jdkInstallHint')}</p>
            </div>
          </div>
          {/* 下载前先测节点存活（FR-229）：避免对离线/卡顿节点发起会卡死的下载 */}
          <div className="flex flex-wrap items-center justify-between gap-2 border-t pt-3">
            {pingSlot}
            <Button onClick={() => void submitInstall()} disabled={installing || majorNum <= 0}>
              <Download className="size-4" />
              {t('nodes.jdkInstall')}
            </Button>
          </div>
        </div>
      )}

      {tab === 'register' && (
        <form onSubmit={submitRegister} className="space-y-3 rounded-md border p-3 text-sm">
          {/* 托管标记置顶：让用户先决定（FR-228） */}
          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={regManaged} onCheckedChange={(v) => setRegManaged(v === true)} aria-label={t('nodes.jdkMarkManaged')} />
            {t('nodes.jdkMarkManaged')}
          </label>

          {/* 路径：可手输 + 浏览（模态）+ 检测；后端探测自动填厂商/版本/架构（FR-228 细化：允许手动输入） */}
          <div className="space-y-1">
            <span className="font-medium">{t('nodes.jdkPath')}<span className="ml-0.5 text-destructive">*</span></span>
            <div className="flex flex-wrap items-center gap-2">
              <Input
                value={pathInput}
                onChange={(e) => setPathInput(e.target.value)}
                placeholder="/opt/jdks/temurin-21 或 .../bin/java"
                className="h-8 min-w-0 flex-1 font-mono"
              />
              <Button type="button" variant="outline" size="sm" onClick={() => setPickerOpen(true)}>
                <FolderOpen className="size-4" />
                {t('artifactCache.browse', '浏览')}
              </Button>
              <Button type="button" variant="outline" size="sm" disabled={!pathInput.trim() || probing} onClick={() => void runProbe(pathInput.trim())}>
                {probing ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
                {t('nodes.jdkDetect', '检测')}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">{t('nodes.jdkProbeHint', '可手动输入 JDK 目录 / java 路径，或点「浏览」选目录；点「检测」后端自动探测厂商 / 版本 / 架构，无需手填。')}</p>
          </div>

          {/* 探测结果：有效 → 只读展示；无效 → 错误 */}
          {probed && !probing && (
            probed.valid ? (
              <dl className="divide-y rounded-md border bg-muted/30 text-sm">
                <ProbeRow label={t('nodes.jdkPath')} value={probed.javaHome} mono />
                <ProbeRow label={t('nodes.jdkVendor')} value={probed.vendor} />
                <ProbeRow label={t('nodes.jdkMajor')} value={String(probed.majorVersion)} />
                <ProbeRow label={t('nodes.jdkVersion')} value={probed.version} />
                <ProbeRow label={t('nodes.jdkArch')} value={probed.arch} />
              </dl>
            ) : (
              <p className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs text-destructive">
                {t('nodes.jdkProbeInvalid', '所选目录不是有效的 JDK')}{probed.error ? `: ${probed.error}` : ''}
              </p>
            )
          )}

          <div className="flex justify-end">
            <Button type="submit" disabled={creating || !probed?.valid}>
              {creating ? t('common.saving') : t('common.save')}
            </Button>
          </div>

          {/* 目录选择器：模态承载（FR-228，不内联动布局），实现由外壳注入（自带取数） */}
          <Dialog open={pickerOpen} onOpenChange={setPickerOpen}>
            <DialogContent className="sm:max-w-lg">
              <DialogHeader>
                <DialogTitle>{t('nodes.jdkSelectDir', '选择 JDK 目录')}</DialogTitle>
              </DialogHeader>
              {renderDirectoryPicker?.({ onPick: onPickPath, onCancel: () => setPickerOpen(false) })}
            </DialogContent>
          </Dialog>
        </form>
      )}

      <DangerConfirm
        open={pendingDel !== null}
        title={pendingDel?.managed
          ? t('nodes.jdkDeleteFilesTitle', '删除 JDK（含文件）?')
          : t('nodes.jdkDeleteRecordTitle', '删除 JDK 登记记录?')}
        description={pendingDel?.managed
          ? t('nodes.jdkDeleteFilesDesc', '该 JDK 由平台下载托管，删除将一并移除 Worker 上的文件，不可恢复。请输入「厂商 主版本」确认。')
          : t('nodes.jdkDeleteRecordDesc', '外部登记的 JDK 仅删除平台记录，不影响磁盘上的 JDK 文件。')}
        confirmLabel={t('common.delete')}
        confirmText={pendingDel?.managed ? `${pendingDel?.vendor} ${pendingDel?.majorVersion}` : undefined}
        onConfirm={() => {
          const target = pendingDel!
          setPendingDel(null)
          void onDelete(target)
        }}
        onCancel={() => setPendingDel(null)}
      />

      {/* 编辑登记信息（FR-311）：改厂商/版本/arch/路径，走既有 PUT /nodes/:id/jdks/:jid。 */}
      <Dialog open={editing !== null} onOpenChange={(next) => { if (!next) setEditing(null) }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t('nodes.jdkEditTitle', '编辑 JDK 登记信息')}</DialogTitle>
          </DialogHeader>
          <div className="space-y-3 text-sm">
            <div className="grid grid-cols-2 gap-3">
              <label className="space-y-1">
                <span className="font-medium">{t('nodes.jdkVendor')}</span>
                <Combobox options={VENDOR_OPTIONS} value={editForm.vendor} onChange={(v) => setEditForm((f) => ({ ...f, vendor: v }))} />
              </label>
              <label className="space-y-1">
                <span className="font-medium">{t('nodes.jdkMajor')}</span>
                <Input value={editForm.majorVersion} inputMode="numeric" aria-label={t('nodes.jdkMajor')}
                  onChange={(e) => setEditForm((f) => ({ ...f, majorVersion: e.target.value }))} />
              </label>
              <label className="space-y-1">
                <span className="font-medium">{t('nodes.jdkVersion')}</span>
                <Input value={editForm.version} aria-label={t('nodes.jdkVersion')}
                  onChange={(e) => setEditForm((f) => ({ ...f, version: e.target.value }))} />
              </label>
              <label className="space-y-1">
                <span className="font-medium">{t('nodes.jdkArch')}</span>
                <Input value={editForm.arch} aria-label={t('nodes.jdkArch')}
                  onChange={(e) => setEditForm((f) => ({ ...f, arch: e.target.value }))} />
              </label>
            </div>
            <label className="block space-y-1">
              <span className="font-medium">{t('nodes.jdkPath')}</span>
              <Input value={editForm.path} className="font-mono text-xs" aria-label={t('nodes.jdkPath')}
                onChange={(e) => setEditForm((f) => ({ ...f, path: e.target.value }))} />
            </label>
            <div className="flex justify-end gap-2 pt-1">
              <Button variant="outline" onClick={() => setEditing(null)}>{t('common.cancel')}</Button>
              <Button disabled={saving || !editForm.vendor.trim() || !editForm.path.trim()} onClick={() => void submitEdit()}>
                {saving ? t('common.saving', '保存中…') : t('common.save', '保存')}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {/* 运行时分区（FR-298 节点运行时库）：由外壳注入，自成一块、自行取数。 */}
      {runtimeSlot}
    </div>
  )
}
