import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, ShieldCheck, ShieldAlert } from 'lucide-react'
import { useClientSignKey, type ClientSignKeySource } from '@/api/clientDistSignKey'
import { copyToClipboard } from '@/lib/clipboard'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

/** 来源徽章文案（FR-248）：env 注入 / 自动生成 / 开发密钥。 */
const SOURCE_META: Record<
  ClientSignKeySource,
  { key: string; fallback: string; variant: 'default' | 'secondary' | 'outline' }
> = {
  env: { key: 'clientSignKey.sourceEnv', fallback: '环境注入', variant: 'default' },
  generated: { key: 'clientSignKey.sourceGenerated', fallback: '自动生成', variant: 'secondary' },
  dev: { key: 'clientSignKey.sourceDev', fallback: '开发密钥', variant: 'outline' },
}

/**
 * OTA 签名公钥信息卡（FR-248，见 ADR-052）。
 * 展示 manifest 签名公钥（等宽 + 复制）、keyId、来源徽章，并大白话说明用途：
 * 把此公钥填入客户端 updater-core 的信任公钥，客户端才会信任本服务器签发的更新。
 * 只读、只展示公钥（私钥绝不出服务端）。未配置（后端 503）时降级显提示、不崩。
 * i18n（FR-016）+ 暗/亮 + 双主题随既有体系（全程主题 token）。
 */
export default function ClientSignKeyCard() {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useClientSignKey()

  const copy = async () => {
    if (!data?.publicKey) return
    const ok = await copyToClipboard(data.publicKey)
    if (ok) toast.success(t('clientSignKey.copied', '公钥已复制到剪贴板'))
    else toast.error(t('clientSignKey.copyFailed', '复制失败，请手动选择复制'))
  }

  return (
    <div className="rounded-xl border bg-card/40 p-4 space-y-3">
      <div className="flex items-center justify-between gap-2 flex-wrap">
        <h2 className="text-sm font-semibold flex items-center gap-2">
          <ShieldCheck className="size-4 text-primary" />
          {t('clientSignKey.title', '签名公钥')}
        </h2>
        {data && (
          <div className="flex items-center gap-2 text-xs">
            <span className="text-muted-foreground font-mono">{data.keyId}</span>
            <Badge variant={SOURCE_META[data.source].variant}>
              {t(SOURCE_META[data.source].key, SOURCE_META[data.source].fallback)}
            </Badge>
          </div>
        )}
      </div>

      <p className="text-xs text-muted-foreground max-w-3xl">
        {t(
          'clientSignKey.desc',
          '把下面这串公钥填入客户端更新器（updater-core）的信任公钥，客户端才会信任本服务器签发的更新。私钥由服务器持有、绝不外泄；密钥已自动生成并持久化，请勿手动删除 etc/client-sign-key.pem，否则公钥会变、已分发的客户端将无法再更新。',
        )}
      </p>

      {isLoading ? (
        <div className="h-16 rounded-md border bg-muted/40 animate-pulse" />
      ) : isError || !data ? (
        <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs text-amber-700 dark:text-amber-500">
          <ShieldAlert className="size-4 shrink-0 mt-0.5" />
          <span>
            {t(
              'clientSignKey.unavailable',
              '暂无法获取签名公钥（签名器未配置或无权限）。OTA 签名不可用时客户端分发将不可用。',
            )}
          </span>
        </div>
      ) : (
        <div className="flex items-center gap-2 rounded-md border bg-muted/50 p-3">
          <code
            className={cn('flex-1 break-all font-mono text-xs sm:text-sm')}
            data-testid="sign-key-public"
          >
            {data.publicKey}
          </code>
          <Button variant="outline" size="sm" onClick={copy} className="shrink-0">
            <Copy className="size-4" /> {t('clientSignKey.copy', '复制公钥')}
          </Button>
        </div>
      )}
    </div>
  )
}
