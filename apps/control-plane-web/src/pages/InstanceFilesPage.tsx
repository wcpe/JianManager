import { Link, useParams, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { ArrowLeft } from 'lucide-react'
import { useInstance } from '@/api/instances'
import ResourceExplorer from '@/components/explorer/ResourceExplorer'
import { Button } from '@jianmanager/ui/components/button'
import { PageShell } from '@jianmanager/ui/components/layout'

/**
 * 实例文件深链页（FR-376）：`/instances/:id/files?path=&file=&mode=`。
 * 独立于控制台页签，便于浏览器新标签并排；不并入 ADR-067 热缓存 key。
 */
export default function InstanceFilesPage() {
  const { id } = useParams<{ id: string }>()
  const [searchParams] = useSearchParams()
  const { t } = useTranslation()
  const instanceId = Number(id)
  const path = searchParams.get('path') ?? ''
  const file = searchParams.get('file') ?? undefined
  // mode=manage 预留给 Config；MVP 仅 files（spec）
  const { data: instance } = useInstance(
    Number.isFinite(instanceId) && instanceId > 0 ? instanceId : 0,
  )

  if (!Number.isFinite(instanceId) || instanceId <= 0) {
    return <p className="p-4 text-muted-foreground">{t('serverConsole.noInstance')}</p>
  }

  return (
    // 阶段 6 页面迁移：外壳改用 PageShell 的 tool 变体（gap-0 p-0），再补回本页原有的 p-3
    // ——它是自带滚动的文件浏览器工具页，不需要 PageShell 默认的 px-[25px] py-[22px]。
    // data-testid 由 PageShell spread 透传。
    <PageShell variant="tool" className="p-3" data-testid="instance-files-page">
      <header className="mb-2 flex shrink-0 items-center gap-2">
        <Button asChild size="sm" variant="ghost" className="h-8 gap-1 px-2">
          <Link to={`/instances/${instanceId}?tab=resource`}>
            <ArrowLeft className="size-3.5" />
            {t('files.backToConsole')}
          </Link>
        </Button>
        <h1 className="truncate text-sm font-semibold">
          {t('files.filesPageTitle', { name: instance?.name ?? `#${instanceId}` })}
        </h1>
      </header>
      <div className="min-h-0 flex-1">
        <ResourceExplorer
          instanceId={instanceId}
          initialDir={path}
          initialFile={file || undefined}
          draftKey="resource-deeplink"
        />
      </div>
    </PageShell>
  )
}
