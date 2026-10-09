import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowRight, Check, DownloadCloud, Plus } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { deriveReadiness, readinessCompletedCount } from '@/lib/client-readiness'
import type { ClientChannel } from '@/lib/client-channel-types'
/** 空状态大引导卡：说明用途 + 主 CTA「创建第一个分发频道」。 */
export function EmptyChannelsGuide({ onCreate }: { onCreate: () => void }) {
  const { t } = useTranslation()
  return (
    <div className="rounded-xl border border-dashed bg-card/40 p-10 text-center flex flex-col items-center gap-4">
      <span className="grid size-14 place-items-center rounded-full bg-primary/10 text-primary">
        <DownloadCloud className="size-7" />
      </span>
      <div className="space-y-1 max-w-md">
        <h2 className="text-lg font-semibold">{t('clientChannels.emptyTitle', '创建第一个分发频道')}</h2>
        <p className="text-sm text-muted-foreground">
          {t(
            'clientChannels.emptyDesc',
            '分发频道是玩家客户端 OTA 更新的入口：建频道 → 拉取密钥 → 发布版本 → 接入启动器，四步即可让玩家自动收到更新。',
          )}
        </p>
      </div>
      <Button onClick={onCreate} size="lg">
        <Plus className="size-4" /> {t('clientChannels.createFirst', '创建分发频道')}
      </Button>
    </div>
  )
}

/** 频道卡片：当前版本 / 密钥数 + 就绪度小标，点击进入工作台。 */
export function ChannelCard({ channel, onOpen }: { channel: ClientChannel; onOpen: () => void }) {
  const { t } = useTranslation()
  const steps = useMemo(
    () => deriveReadiness({ keyCount: channel.keyCount ?? 0, currentVersion: channel.currentVersion }),
    [channel.keyCount, channel.currentVersion],
  )
  const completed = readinessCompletedCount(steps)
  const ready = completed === steps.length

  return (
    <button
      type="button"
      onClick={onOpen}
      className="group text-left rounded-xl border bg-card/40 p-4 transition-colors hover:border-primary/40 hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="font-semibold truncate">{channel.name}</div>
          <div className="font-mono text-xs text-muted-foreground truncate">{channel.channelId}</div>
        </div>
        <ArrowRight className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
      </div>

      {channel.description && (
        <p className="mt-2 text-xs text-muted-foreground line-clamp-2">{channel.description}</p>
      )}

      <div className="mt-3 flex items-center gap-2 flex-wrap text-xs">
        <Badge variant={channel.currentVersion > 0 ? 'default' : 'outline'}>
          {channel.currentVersion > 0
            ? `v${channel.currentVersion}`
            : t('clientChannels.unpublished', '未发布')}
        </Badge>
        <Badge variant="outline">
          {t('clientChannels.keyCountBadge', '{{n}} 个密钥', { n: channel.keyCount ?? 0 })}
        </Badge>
        {ready ? (
          <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-500">
            <Check className="size-3.5" /> {t('clientChannels.ready', '已就绪')}
          </span>
        ) : (
          <span className="text-muted-foreground">
            {t('clientChannels.readinessShort', '就绪度 {{c}}/{{n}}', { c: completed, n: steps.length })}
          </span>
        )}
      </div>
    </button>
  )
}
