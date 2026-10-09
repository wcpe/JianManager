import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'

/** 常驻 bot.chat 成功边界免责声明（ADR-075）。 */
export function DisclaimerBanner({ className = '' }: { className?: string }) {
  const { t } = useTranslation()
  return (
    <div
      role="note"
      data-testid="bot-chat-disclaimer"
      className={`flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-950 dark:text-amber-100 ${className}`}
    >
      <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
      <p>{t('botLoad.disclaimer')}</p>
    </div>
  )
}
