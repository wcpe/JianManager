import { useState, type FormEvent } from 'react'
import { Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useCacheServerProbeVersion,
  useServerProbeCatalog,
  useSetGlobalProbeVersion,
  useSyncServerProbeSource,
  useUploadServerProbeVersion,
} from '@/api/artifactVersions'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'

/** ServerProbe 首期制品包管理页：管理员手动同步、缓存和选择全局默认。 */
export default function ArtifactVersionsPage() {
  const { t } = useTranslation()
  const { data: catalog, isLoading, isError } = useServerProbeCatalog()
  const sync = useSyncServerProbeSource()
  const upload = useUploadServerProbeVersion()
  const cache = useCacheServerProbeVersion()
  const setDefault = useSetGlobalProbeVersion()
  const [uploadVersion, setUploadVersion] = useState('')
  const [uploadFile, setUploadFile] = useState<File | null>(null)
  const [uploadFileKey, setUploadFileKey] = useState(0)

  if (isLoading) return <div className="p-4 text-sm text-muted-foreground">{t('common.loading')}</div>
  if (isError || !catalog) return <div className="p-4 text-sm text-destructive">{t('artifactVersions.loadFailed')}</div>

  const actionError = (error: unknown) => {
    const message = (error as { response?: { data?: { message?: string } } })?.response?.data?.message
    toast.error(message || t('artifactVersions.actionFailed'))
  }
  const cachedVersions = catalog.versions.filter((version) => version.assetId > 0)
  const sourceByID = new Map(catalog.sources.map((source) => [source.id, source]))
  const sourceLabel = (provider: string, fallback: string) => {
    if (provider === 'github-release') return t('artifactVersions.githubReleaseSource')
    if (provider === 'local-upload') return t('artifactVersions.localUploadSource')
    return fallback
  }
  const submitUpload = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!uploadFile || !uploadVersion.trim()) return
    upload.mutate({ version: uploadVersion.trim(), file: uploadFile }, {
      onSuccess: () => {
        setUploadVersion('')
        setUploadFile(null)
        setUploadFileKey((key) => key + 1)
        toast.success(t('artifactVersions.uploadSucceeded'))
      },
      onError: actionError,
    })
  }

  return (
    <div className="mx-auto max-w-5xl space-y-4">
      <div>
        <h1 className="text-xl font-semibold">{t('artifactVersions.title')}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t('artifactVersions.description')}</p>
      </div>

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
                  disabled={sync.isPending || !source.enabled}
                  onClick={() => sync.mutate(source.id, { onSuccess: (result) => toast.success(t('artifactVersions.synced', { count: result.created })), onError: actionError })}
                >
                  {sync.isPending && <Loader2 className="mr-1 size-3.5 animate-spin" />}
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
          <Button type="submit" disabled={upload.isPending || !uploadVersion.trim() || !uploadFile}>
            {upload.isPending && <Loader2 className="mr-1 size-3.5 animate-spin" />}
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
                  <Button size="sm" variant="outline" disabled={cache.isPending} onClick={() => cache.mutate(version.id, { onSuccess: () => toast.success(t('artifactVersions.cacheSucceeded')), onError: actionError })}>
                    {cache.isPending && <Loader2 className="mr-1 size-3.5 animate-spin" />}
                    {t('artifactVersions.cache')}
                  </Button>
                ) : (
                  <Button size="sm" variant={isDefault ? 'secondary' : 'outline'} disabled={isDefault || setDefault.isPending} onClick={() => setDefault.mutate(version.id, { onSuccess: () => toast.success(t('artifactVersions.defaultSaved')), onError: actionError })}>
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
    </div>
  )
}
