import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, Download, FolderOpen, PackageCheck } from 'lucide-react'
import { useNodeJDKs, useCreateJDK, useDeleteJDK, useInstallJDK, type NodeJDK } from '@/api/jdks'
import { useJDKCatalog } from '@/api/nodeRuntime'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldError } from '@/components/ui/field-label'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import DangerConfirm from '@/components/DangerConfirm'
import DirectoryPicker from '@/components/DirectoryPicker'
import { validateAbsPath, validatePositiveInt } from '@/lib/form-validation'
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
  const { data: jdks, isLoading } = useNodeJDKs(nodeId)
  const create = useCreateJDK(nodeId)
  const del = useDeleteJDK(nodeId)
  const install = useInstallJDK(nodeId)

  const [tab, setTab] = useState<JDKTab>('list')

  // 安装表单状态。
  const [vendor, setVendor] = useState('Temurin')
  const [major, setMajor] = useState('21')
  const [version, setVersion] = useState('') // 具体版本（空=该大版本最新）
  const [arch, setArch] = useState('x64')

  // 登记表单状态。
  const [regVendor, setRegVendor] = useState('Temurin')
  const [regMajor, setRegMajor] = useState('21')
  const [regVersion, setRegVersion] = useState('')
  const [regArch, setRegArch] = useState('x64')
  const [regPath, setRegPath] = useState('')
  const [regManaged, setRegManaged] = useState(false)
  const [showPicker, setShowPicker] = useState(false)

  const [pendingDel, setPendingDel] = useState<NodeJDK | null>(null)

  // foojay 版本目录（仅在「一键下载」分段且 vendor 非空时查询）。
  const majorNum = Number(major) || 0
  const catalog = useJDKCatalog(nodeId, vendor, majorNum, { enabled: active && tab === 'install' })

  const regPathError = validateAbsPath(regPath)
  const regMajorError = validatePositiveInt(regMajor)
  const registerInvalid = regPath.trim() === '' || !!regPathError || !!regMajorError

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
    if (registerInvalid) return
    create.mutate(
      { vendor: regVendor, majorVersion: Number(regMajor), version: regVersion, arch: regArch, path: regPath, managed: regManaged },
      {
        onSuccess: () => {
          toast.success(t('nodes.jdkRegistered'))
          setRegVersion('')
          setRegPath('')
          setShowPicker(false)
          setTab('list')
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(err.response?.data?.message || t('nodes.jdkRegisterFailed')),
      }
    )
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
        isLoading ? (
          <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : !jdks || jdks.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('nodes.jdkEmpty')}</p>
        ) : (
          <div className="overflow-x-auto rounded-md border">
            <table className="w-full text-sm">
              <thead className="bg-muted">
                <tr>
                  <th className="px-2 py-1.5 text-left font-medium">{t('nodes.jdkVendor')}</th>
                  <th className="px-2 py-1.5 text-left font-medium">{t('nodes.jdkMajor')}</th>
                  <th className="px-2 py-1.5 text-left font-medium">{t('nodes.jdkVersion')}</th>
                  <th className="px-2 py-1.5 text-left font-medium">{t('nodes.jdkArch')}</th>
                  <th className="px-2 py-1.5 text-left font-medium">{t('nodes.jdkPath')}</th>
                  <th className="px-2 py-1.5 text-left font-medium">{t('nodes.jdkSource')}</th>
                  <th className="px-2 py-1.5 text-right font-medium">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {jdks.map((j) => (
                  <tr key={j.id} className="border-t">
                    <td className="px-2 py-1.5">{j.vendor}</td>
                    <td className="px-2 py-1.5">{j.majorVersion}</td>
                    <td className="px-2 py-1.5">{j.version || '—'}</td>
                    <td className="px-2 py-1.5">{j.arch || '—'}</td>
                    <td className="px-2 py-1.5">
                      <button
                        type="button"
                        className="flex max-w-[16rem] items-center gap-1 font-mono text-xs text-muted-foreground hover:text-foreground"
                        title={j.path}
                        onClick={() => copyPath(j.path)}
                      >
                        <span className="truncate">{j.path}</span>
                        <Copy className="size-3 shrink-0" />
                      </button>
                    </td>
                    <td className="px-2 py-1.5 text-xs">{j.managed ? t('nodes.jdkManaged') : t('nodes.jdkExternal')}</td>
                    <td className="px-2 py-1.5 text-right">
                      <button
                        type="button"
                        className="text-xs text-destructive hover:underline"
                        onClick={() => setPendingDel(j)}
                      >
                        {t('common.delete')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
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
          <div className="flex justify-end border-t pt-3">
            <Button onClick={onInstall} disabled={install.isPending || majorNum <= 0}>
              <Download className="size-4" />
              {t('nodes.jdkInstall')}
            </Button>
          </div>
        </div>
      )}

      {tab === 'register' && (
        <form onSubmit={onRegister} className="space-y-3 rounded-md border p-3 text-sm">
          <div className="grid grid-cols-2 gap-3">
            <label className="space-y-1">
              <span className="font-medium">{t('nodes.jdkVendor')}</span>
              <Combobox options={VENDOR_OPTIONS} value={regVendor} onChange={setRegVendor} />
            </label>
            <div className="space-y-1">
              <span className="font-medium">{t('nodes.jdkMajor')}<span className="ml-0.5 text-destructive">*</span></span>
              <Input value={regMajor} onChange={(e) => setRegMajor(e.target.value)} inputMode="numeric" aria-invalid={!!regMajorError} />
              <FieldError error={regMajorError} />
            </div>
            <label className="space-y-1">
              <span className="font-medium">{t('nodes.jdkVersion')}</span>
              <Input value={regVersion} onChange={(e) => setRegVersion(e.target.value)} placeholder="21.0.4" />
            </label>
            <label className="space-y-1">
              <span className="font-medium">{t('nodes.jdkArch')}</span>
              <Combobox options={ARCH_OPTIONS} value={regArch} onChange={setRegArch} />
            </label>
          </div>

          <div className="space-y-1">
            <span className="font-medium">{t('nodes.jdkPath')}<span className="ml-0.5 text-destructive">*</span></span>
            <div className="flex gap-2">
              <Input
                value={regPath}
                onChange={(e) => setRegPath(e.target.value)}
                aria-invalid={!!regPathError}
                placeholder="/opt/jdks/temurin-21"
                className="flex-1"
              />
              <Button type="button" variant="outline" size="sm" onClick={() => setShowPicker((v) => !v)}>
                <FolderOpen className="size-4" />
                {t('artifactCache.browse')}
              </Button>
            </div>
            <FieldError error={regPathError} />
          </div>

          {/* 目录选择器：稳定子视图（在表单内固定位置展开，不致面板其余内容重排） */}
          {showPicker && (
            <DirectoryPicker
              nodeId={nodeId}
              initialPath={regPath}
              onPick={(p) => { setRegPath(p); setShowPicker(false) }}
              onCancel={() => setShowPicker(false)}
            />
          )}

          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={regManaged} onCheckedChange={(v) => setRegManaged(v === true)} aria-label={t('nodes.jdkMarkManaged')} />
            {t('nodes.jdkMarkManaged')}
          </label>
          <div className="flex justify-end">
            <Button type="submit" disabled={create.isPending || registerInvalid}>
              {create.isPending ? t('common.saving') : t('common.save')}
            </Button>
          </div>
        </form>
      )}

      <DangerConfirm
        open={pendingDel !== null}
        title={t('nodes.jdkDeleteConfirm', { vendor: pendingDel?.vendor, major: pendingDel?.majorVersion })}
        description={t('nodes.jdkDeleteDescription')}
        confirmLabel={t('common.delete')}
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
    </div>
  )
}
