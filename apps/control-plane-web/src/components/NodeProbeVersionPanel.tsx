import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useNodeProbeVersion, useServerProbeCatalog, useSetNodeProbeVersion } from '@/api/artifactVersions'

interface NodeProbeVersionPanelProps {
  nodeId: number
  active?: boolean
}

/** Worker 默认探针版本仅用于之后新建的实例，不会自动改动已有实例。 */
export default function NodeProbeVersionPanel({ nodeId, active = true }: NodeProbeVersionPanelProps) {
  const { t } = useTranslation()
  const { data: catalog, isLoading: catalogLoading } = useServerProbeCatalog({ enabled: active })
  const { data: selected, isLoading: selectionLoading } = useNodeProbeVersion(nodeId, { enabled: active })
  const update = useSetNodeProbeVersion(nodeId)
  const versions = (catalog?.versions ?? []).filter((version) => version.assetId > 0)

  if (catalogLoading || selectionLoading) return <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
  if (!catalog || !selected) return <p className="text-sm text-destructive">{t('probe.nodeLoadFailed')}</p>

  const save = (value: string) => {
    update.mutate(Number(value), {
      onSuccess: () => toast.success(t('probe.nodeVersionSaved')),
      onError: (error) => {
        const message = (error as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(message || t('probe.nodeVersionSaveFailed'))
      },
    })
  }

  return (
    <div className="space-y-3">
      <div>
        <h3 className="text-sm font-semibold">{t('probe.nodeVersionTitle')}</h3>
        <p className="mt-1 text-xs text-muted-foreground">{t('probe.nodeVersionHint')}</p>
      </div>
      <select
        className="h-9 w-full max-w-md rounded-md border bg-background px-2 text-sm"
        value={String(selected.versionId)}
        disabled={update.isPending}
        onChange={(event) => save(event.target.value)}
      >
        <option value="0">{t('probe.nodeInherit', { version: catalog.versions.find((version) => version.id === catalog.package.defaultVersionId)?.version || '—' })}</option>
        {versions.map((version) => <option key={version.id} value={version.id}>{version.version}</option>)}
      </select>
    </div>
  )
}
