import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, Download, FolderOpen, Loader2, Package, PackageCheck, Pencil, Coffee, Search, Trash2 } from 'lucide-react'
import { useNodeJDKs, useCreateJDK, useDeleteJDK, useInstallJDK, useProbeJDK, useUpdateJDK, type NodeJDK, type ProbeResult } from '@/api/jdks'
import { useJDKCatalog } from '@/api/nodeRuntime'
import { PingNodeButton } from '@/components/PingNodeButton'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Badge } from '@jianmanager/ui/components/badge'
import { ViewToggle, type ViewMode } from '@jianmanager/ui/components/view-toggle'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/DangerConfirm'
import DirectoryPicker from '@/components/DirectoryPicker'
import NodeRuntimeSection from '@/components/NodeRuntimeSection'
import { copyToClipboard } from '@/lib/clipboard'

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

interface NodeJDKPanelProps {
  nodeId: number
  /** 是否启用查询（抽屉/分段打开时为 true）。 */
  active?: boolean
}

/** 面板内子视图：已登记列表 / 一键下载 / 登记已有（分段切换，容器固定不重排）。 */
type JDKTab = 'list' | 'install' | 'register'

/**
 * 节点 JDK 管理面板（FR-178 重做）：
 * - 表格横向不溢出（overflow-x-auto）、路径可复制、删除走 DangerConfirm；
 * - 一键下载支持多厂商 + foojay 具体版本选择器，下发接任务中心（FR-183）；
 * - 登记已有用目录选择器选路径（非手敲）。
 * 三个动作以分段切换（容器稳定，符合抽屉 UX 约束：不切换隐显内联表单致布局重组）。
 * 可复用独立组件（不绑死容器，便于 FR-177 改挂右栏分段）。
 */
export default function NodeJDKPanel({ nodeId, active = true }: NodeJDKPanelProps) {
  const { t } = useTranslation()
  const { data: jdks, isLoading, isFetching } = useNodeJDKs(nodeId)
  const create = useCreateJDK(nodeId)
  const del = useDeleteJDK(nodeId)
  const install = useInstallJDK(nodeId)

  const [tab, setTab] = useState<JDKTab>('list')

  // 安装表单状态。
  const [vendor, setVendor] = useState('Temurin')
  const [major, setMajor] = useState('21')
  const [version, setVersion] = useState('') // 具体版本（空=该大版本最新）
  const [arch, setArch] = useState('x64')

  // 登记表单状态（FR-228：选目录 + 后端探测自动填，不再手填厂商/版本/架构）。
  const [regManaged, setRegManaged] = useState(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [probed, setProbed] = useState<ProbeResult | null>(null)
  const [pathInput, setPathInput] = useState('') // 登记路径：可手输或经选目录填入（FR-228 细化）
  const probe = useProbeJDK(nodeId)

  const [pendingDel, setPendingDel] = useState<NodeJDK | null>(null)
  const [view, setView] = useState<ViewMode>('list')
  const [query, setQuery] = useState('')
  const [sourceFilter, setSourceFilter] = useState<'all' | 'managed' | 'external'>('all')

  // 编辑登记信息（FR-311）：行内铅笔 → 模态改厂商/大版本/具体版本/arch/路径，走既有 PUT。
  const update = useUpdateJDK(nodeId)
  const [editing, setEditing] = useState<NodeJDK | null>(null)
  const [editForm, setEditForm] = useState({ vendor: '', majorVersion: '', version: '', arch: '', path: '' })
  const openEdit = (j: NodeJDK) => {
    setEditing(j)
    setEditForm({ vendor: j.vendor, majorVersion: String(j.majorVersion), version: j.version ?? '', arch: j.arch ?? '', path: j.path })
  }
  const submitEdit = () => {
    if (!editing) return
    update.mutate(
      {
        jdkId: editing.id,
        body: {
          vendor: editForm.vendor.trim(),
          majorVersion: Number(editForm.majorVersion) || editing.majorVersion,
          version: editForm.version.trim(),
          arch: editForm.arch.trim(),
          path: editForm.path.trim(),
        },
      },
      {
        onSuccess: () => { toast.success(t('nodes.jdkEditSaved', '已保存 JDK 登记信息')); setEditing(null) },
        onError: (err: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(err.response?.data?.message || t('nodes.jdkEditFailed', '保存失败')),
      },
    )
  }

  // foojay 版本目录（仅在「一键下载」分段且 vendor 非空时查询）。
  const majorNum = Number(major) || 0
  const catalog = useJDKCatalog(nodeId, vendor, majorNum, { enabled: active && tab === 'install' })

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

  const onInstall = () => {
    install.mutate(
      { vendor, majorVersion: majorNum, arch, version: version.trim() || undefined },
      {
        // FR-183：异步任务，回执 taskId；进度/完成在「任务中心」与站内信查看。
        onSuccess: () => {
          toast.success(t('artifactCache.jdkInstallDispatched'))
          setTab('list')
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(err.response?.data?.message || t('nodes.jdkInstallFailed')),
      }
    )
  }

  const onRegister = (e: FormEvent) => {
    e.preventDefault()
    if (!probed?.valid) return
    create.mutate(
      {
        vendor: probed.vendor,
        majorVersion: probed.majorVersion,
        version: probed.version,
        arch: probed.arch,
        path: probed.javaHome,
        managed: regManaged,
      },
      {
        onSuccess: () => {
          toast.success(t('nodes.jdkRegistered'))
          setProbed(null)
          setPathInput('')
          setRegManaged(false)
          setTab('list')
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(err.response?.data?.message || t('nodes.jdkRegisterFailed')),
      }
    )
  }

  // 探测路径（FR-228）：后端 java -version 自动得出厂商/版本/架构，结果存 probed。手输或选目录都走这里。
  const runProbe = (path: string) => {
    if (!path) return
    probe.mutate(path, {
      onSuccess: (res) => setProbed(res),
      onError: (err: Error & { response?: { data?: { message?: string } } }) => {
        setProbed(null)
        toast.error(err.response?.data?.message || t('nodes.jdkProbeFailed', '探测失败'))
      },
    })
  }

  // 选目录后填入路径并自动探测（FR-228）。
  const onPickPath = (path: string) => {
    setPickerOpen(false)
    setPathInput(path)
    runProbe(path)
  }

  const copyPath = async (p: string) => {
    const ok = await copyToClipboard(p)
    if (ok) toast.success(t('artifactCache.pathCopied'))
    else toast.error(t('common.copyFailed'))
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
            <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
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
                      onClick={() => copyPath(j.path)}
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
                    onClick={() => copyPath(j.path)}
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
              {catalog.isLoading ? (
                <p className="px-1 py-1.5 text-xs text-muted-foreground">{t('artifactCache.jdkCatalogLoading')}</p>
              ) : catalog.isError || !catalog.data || catalog.data.length === 0 ? (
                // foojay 不可达/无结果：降级为手填具体版本（仍可下载）。
                <Input value={version} onChange={(e) => setVersion(e.target.value)} placeholder={t('artifactCache.jdkVersionLatest')} />
              ) : (
                <Combobox
                  options={[
                    { value: '', label: t('artifactCache.jdkVersionLatest') },
                    ...catalog.data.map((p) => ({
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
            <PingNodeButton nodeId={nodeId} />
            <Button onClick={onInstall} disabled={install.isPending || majorNum <= 0}>
              <Download className="size-4" />
              {t('nodes.jdkInstall')}
            </Button>
          </div>
        </div>
      )}

      {tab === 'register' && (
        <form onSubmit={onRegister} className="space-y-3 rounded-md border p-3 text-sm">
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
              <Button type="button" variant="outline" size="sm" disabled={!pathInput.trim() || probe.isPending} onClick={() => runProbe(pathInput.trim())}>
                {probe.isPending ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
                {t('nodes.jdkDetect', '检测')}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">{t('nodes.jdkProbeHint', '可手动输入 JDK 目录 / java 路径，或点「浏览」选目录；点「检测」后端自动探测厂商 / 版本 / 架构，无需手填。')}</p>
          </div>

          {/* 探测结果：有效 → 只读展示；无效 → 错误 */}
          {probed && !probe.isPending && (
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
            <Button type="submit" disabled={create.isPending || !probed?.valid}>
              {create.isPending ? t('common.saving') : t('common.save')}
            </Button>
          </div>

          {/* 目录选择器：模态承载（FR-228，不内联动布局） */}
          <Dialog open={pickerOpen} onOpenChange={setPickerOpen}>
            <DialogContent className="sm:max-w-lg">
              <DialogHeader>
                <DialogTitle>{t('nodes.jdkSelectDir', '选择 JDK 目录')}</DialogTitle>
              </DialogHeader>
              <DirectoryPicker nodeId={nodeId} onPick={onPickPath} onCancel={() => setPickerOpen(false)} />
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
          const id = pendingDel!.id
          setPendingDel(null)
          del.mutate(id, {
            onSuccess: () => toast.success(t('nodes.jdkDeleted')),
            onError: (err: Error & { response?: { data?: { message?: string; instances?: { name: string }[] } } }) => {
              const insts = err.response?.data?.instances
              if (insts && insts.length > 0) {
                toast.error(t('nodes.jdkInUse', { names: insts.map((i) => i.name).join(', ') }))
              } else {
                toast.error(err.response?.data?.message || t('nodes.jdkDeleteFailed'))
              }
            },
          })
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
              <Button disabled={update.isPending || !editForm.vendor.trim() || !editForm.path.trim()} onClick={submitEdit}>
                {update.isPending ? t('common.saving', '保存中…') : t('common.save', '保存')}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {/* 运行时分区（FR-298 节点运行时库）：统一列表（类型徽章）+ 扫描发现候选勾选入库。 */}
      <NodeRuntimeSection nodeId={nodeId} active={active} />
    </div>
  )
}
