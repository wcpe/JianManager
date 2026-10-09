/**
 * @file ArtifactVersionsPageView：ServerProbe 制品版本库页的受控视图，目录取数与同步/上传/缓存/设默认四个写动作由应用容器负责。
 * @input Panel/Input/Button 原语、PageShell/PageHeader 布局原语、翻译上下文
 * @output ArtifactVersionsPageView、ArtifactVersionsPageViewProps、ArtifactCatalogView、ArtifactSourceView、ArtifactVersionView
 * @sync apps/control-plane-web/src/pages/ArtifactVersionsPage.tsx、apps/control-plane-web/src/pages/ArtifactVersionsPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-409 制品版本库 + FR-411 本地上传来源）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Loader2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'

/**
 * 制品来源（本视图只消费展示所需字段）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 API 层的 `ArtifactSource`：外壳直接传完整对象
 * 也结构兼容，从而无需把制品库的整套类型迁进包（同 `NodeProbeVersionPanel` 的取舍）。
 */
export interface ArtifactSourceView {
  id: number
  provider: string
  name: string
  enabled: boolean
  lastSyncedAt: string | null
  lastError: string
}

/** 制品版本（本视图只消费展示所需字段）。 */
export interface ArtifactVersionView {
  id: number
  sourceId: number
  version: string
  expectedSha256: string
  /** >0 表示已落制品库（可设默认）；0 表示尚未缓存（仅可缓存）。 */
  assetId: number
  lastError: string
}

/** 制品目录（本视图只消费展示所需字段）。 */
export interface ArtifactCatalogView {
  package: { defaultVersionId: number }
  sources: ArtifactSourceView[]
  versions: ArtifactVersionView[]
}

/**
 * 受控边界：目录数据与加载/错误态经 props 注入；四个写动作以回调上报，由外壳执行 mutation
 * 并决定成功/失败文案——包内不取数、不发请求、不弹 toast。
 * 本地上传的表单草稿与文件选择是纯 UI 状态，留在视图内；因「成功才清空表单」，
 * 该回调返回 `Promise<boolean>`（包内既有惯例，见 `NodeProbeVersionPanel`）。
 */
export interface ArtifactVersionsPageViewProps {
  /** 制品目录（包 + 来源 + 版本）；外壳取数后注入。 */
  catalog?: ArtifactCatalogView
  /** 加载态；外壳注入。 */
  isLoading?: boolean
  /** 错误态；外壳注入。 */
  isError?: boolean
  /** 线上来源同步在途。 */
  syncPending?: boolean
  /** 本地上传在途。 */
  uploadPending?: boolean
  /** 缓存（下载入制品库）在途。 */
  cachePending?: boolean
  /** 设为全局默认在途。 */
  defaultPending?: boolean
  /** 同步某个线上来源新版本。 */
  onSync: (sourceId: number) => void
  /** 上传本地 jar；返回是否成功（成功才清空版本号与文件选择）。 */
  onUpload: (payload: { version: string; file: File }) => Promise<boolean>
  /** 缓存某个版本（CP 下载入制品库）。 */
  onCache: (versionId: number) => void
  /** 设为全局默认版本。 */
  onSetGlobalDefault: (versionId: number) => void
}

/** ServerProbe 制品版本库：来源同步、本地上传与全局默认版本选择。 */
export function ArtifactVersionsPageView({
  catalog,
  isLoading,
  isError,
  syncPending = false,
  uploadPending = false,
  cachePending = false,
  defaultPending = false,
  onSync,
  onUpload,
  onCache,
  onSetGlobalDefault,
}: ArtifactVersionsPageViewProps) {
  const { t } = useTranslation()
  const [uploadVersion, setUploadVersion] = useState('')
  const [uploadFile, setUploadFile] = useState<File | null>(null)
  // 文件选择框的 key：上传成功后自增以清空浏览器已选文件（DOM 值不受受控 prop 约束）。
  const [uploadFileKey, setUploadFileKey] = useState(0)

  if (isLoading) return <div className="p-4 text-sm text-muted-foreground">{t('common.loading')}</div>
  if (isError || !catalog) return <div className="p-4 text-sm text-destructive">{t('artifactVersions.loadFailed')}</div>

  const cachedVersions = catalog.versions.filter((version) => version.assetId > 0)
  const sourceByID = new Map(catalog.sources.map((source) => [source.id, source]))
  const sourceLabel = (provider: string, fallback: string) => {
    if (provider === 'github-release') return t('artifactVersions.githubReleaseSource')
    if (provider === 'local-upload') return t('artifactVersions.localUploadSource')
    return fallback
  }
  const submitUpload = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!uploadFile || !uploadVersion.trim()) return
    const ok = await onUpload({ version: uploadVersion.trim(), file: uploadFile })
    // 成功才复位：失败时保留已填版本号与文件，便于直接重试。
    if (!ok) return
    setUploadVersion('')
    setUploadFile(null)
    setUploadFileKey((key) => key + 1)
  }

  return (
    // 全量对齐：外壳与页头改用布局层原语。保留 mx-auto max-w-5xl——本页是窄栏内容页
    // （与创建实例向导同处理）。原先无 data-page，迁移时补上。
    <PageShell data-page="artifact-versions" className="mx-auto max-w-5xl">
      <PageHeader
        title={t('artifactVersions.title')}
        description={t('artifactVersions.description')}
      />

      <Panel title={t('artifactVersions.sources')}>
        <div className="divide-y">
          {catalog.sources.map((source) => (
            <div key={source.id} className="flex flex-wrap items-center gap-3 p-3 text-sm">
              <div className="min-w-44 flex-1">
                <p className="font-medium">{sourceLabel(source.provider, source.name)}</p>
                {source.provider === 'github-release' && <p className="text-xs text-muted-foreground">{source.lastSyncedAt ? new Date(source.lastSyncedAt).toLocaleString() : t('artifactVersions.neverSynced')}</p>}
                {source.lastError && <p className="mt-1 text-xs text-destructive">{source.lastError}</p>}
              </div>
              {source.provider === 'github-release' && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={syncPending || !source.enabled}
                  onClick={() => onSync(source.id)}
                >
                  {syncPending && <Loader2 className="mr-1 size-3.5 animate-spin" />}
                  {t('artifactVersions.sync')}
                </Button>
              )}
            </div>
          ))}
        </div>
      </Panel>

      <Panel title={t('artifactVersions.localUpload')}>
        <form className="flex flex-wrap items-end gap-3 p-3" onSubmit={submitUpload}>
          <label className="min-w-44 flex-1 text-sm">
            <span className="mb-1 block">{t('artifactVersions.uploadVersion')}</span>
            <Input value={uploadVersion} onChange={(event) => setUploadVersion(event.target.value)} placeholder={t('artifactVersions.uploadVersionPlaceholder')} />
          </label>
          <label className="min-w-64 flex-1 text-sm">
            <span className="mb-1 block">{t('artifactVersions.uploadFile')}</span>
            <Input key={uploadFileKey} type="file" accept=".jar,application/java-archive" onChange={(event) => setUploadFile(event.target.files?.[0] ?? null)} />
          </label>
          <Button type="submit" disabled={uploadPending || !uploadVersion.trim() || !uploadFile}>
            {uploadPending && <Loader2 className="mr-1 size-3.5 animate-spin" />}
            {t('artifactVersions.upload')}
          </Button>
        </form>
      </Panel>

      <Panel title={t('artifactVersions.versions')}>
        <div className="divide-y">
          {catalog.versions.map((version) => {
            const cached = version.assetId > 0
            const isDefault = version.id === catalog.package.defaultVersionId
            const source = sourceByID.get(version.sourceId)
            return (
              <div key={version.id} className="flex flex-wrap items-center gap-3 p-3 text-sm">
                <div className="min-w-44 flex-1">
                  <p className="font-medium">{version.version}{isDefault ? ` · ${t('artifactVersions.globalDefault')}` : ''}</p>
                  {source && <p className="text-xs text-muted-foreground">{sourceLabel(source.provider, source.name)}</p>}
                  <p className="text-xs text-muted-foreground">{cached ? `${t('artifactVersions.cached')} · ${version.expectedSha256.slice(0, 12)}` : t('artifactVersions.notCached')}</p>
                  {version.lastError && <p className="mt-1 text-xs text-destructive">{version.lastError}</p>}
                </div>
                {!cached ? (
                  <Button size="sm" variant="outline" disabled={cachePending} onClick={() => onCache(version.id)}>
                    {cachePending && <Loader2 className="mr-1 size-3.5 animate-spin" />}
                    {t('artifactVersions.cache')}
                  </Button>
                ) : (
                  <Button size="sm" variant={isDefault ? 'secondary' : 'outline'} disabled={isDefault || defaultPending} onClick={() => onSetGlobalDefault(version.id)}>
                    {t('artifactVersions.setGlobalDefault')}
                  </Button>
                )}
              </div>
            )
          })}
          {catalog.versions.length === 0 && <p className="p-4 text-sm text-muted-foreground">{t('artifactVersions.empty')}</p>}
        </div>
      </Panel>

      <p className="text-xs text-muted-foreground">{t('artifactVersions.rolloutHint', { count: cachedVersions.length })}</p>
    </PageShell>
  )
}
