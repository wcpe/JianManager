import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Check, History } from 'lucide-react'
import { useUpdaterCoreVersions, useSelectUpdaterCore } from '@/api/clientChannels'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import DangerConfirm from '@/components/DangerConfirm'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * updater-core 版本选择器（FR-259）。频道工作台「Core 版本」tab：
 * 列出所有归档 core 版本，当前选定版本高亮，一键切换实现回滚。
 * 切换后提示"客户端下次启动生效"。
 */
export default function ClientUpdaterCoreSelector({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const { data: versions, isLoading } = useUpdaterCoreVersions(channelId)
  const select = useSelectUpdaterCore()
  const [target, setTarget] = useState<string | null>(null)

  const selectedVersion = versions?.find((v) => v.selected)
  const latestVersion = versions?.[0]

  const doSelect = () => {
    if (!target) return
    const sha = target
    setTarget(null)
    select.mutate(
      { channelId, sha256: sha },
      {
        onSuccess: () => toast.success(t('clientCore.switched', '已切换，客户端下次启动生效')),
        onError: (e) => toast.error(errMsg(e, t('clientCore.switchFailed', '切换失败'))),
      },
    )
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
      </div>

      <div className="grid gap-2 sm:grid-cols-2">
        <div className="rounded-lg border bg-card p-3">
          <div className="text-[11px] text-muted-foreground">{t('clientCore.latestArchived', '最新归档版本')}</div>
          <div className="mt-1 font-mono text-lg font-semibold">
            {latestVersion ? `v${latestVersion.version}` : '—'}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground">
            {latestVersion ? `${latestVersion.sha256.slice(0, 12)}… · ${formatBytes(latestVersion.size)}` : t('clientCore.noVersionsShort', '暂无归档')}
          </div>
        </div>
        <div className="rounded-lg border bg-card p-3">
          <div className="text-[11px] text-muted-foreground">{t('clientCore.currentSelected', '当前选定版本')}</div>
          <div className="mt-1 font-mono text-lg font-semibold">
            {selectedVersion ? `v${selectedVersion.version}` : latestVersion ? t('clientCore.followLatest', '跟随最新') : '—'}
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
                    v{v.version}
                  </span>
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  {v.sha256.slice(0, 12)}…
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
                        disabled={select.isPending}
                        onClick={() => setTarget(v.sha256)}
                      >
                        {t('clientCore.select', '选定')}
                      </Button>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            ))}
            {versions?.length === 0 && !isLoading && (
              <TableRow>
                <TableCell colSpan={5} className="h-16 text-center text-muted-foreground">
                  {t('clientCore.noVersions', '暂无归档版本（需 make embed-client-updater 构建后启动 CP 归档）')}
                </TableCell>
              </TableRow>
            )}
            {isLoading && (
              <TableRow>
                <TableCell colSpan={5} className="h-16 text-center text-muted-foreground">
                  {t('common.loading', '加载中…')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <DangerConfirm
        open={target !== null}
        title={t('clientCore.switchConfirm', '切换 updater-core 版本？')}
        description={t(
          'clientCore.switchDesc',
          '切换后客户端下次启动按 endpoint 自动查询并使用该版本。本地已有该版本 jar 的客户端直接用、没有的自动下载。请确认确需切换。',
        )}
        scope="platform"
        confirmLabel={t('clientCore.select', '选定')}
        onConfirm={doSelect}
        onCancel={() => setTarget(null)}
      />
    </div>
  )
}

/** formatBytes 把字节数格式化为人类可读（KB/MB）。 */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
