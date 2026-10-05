import { useTranslation } from 'react-i18next'

/** 工作区空状态：未打开任何实例终端时的提示。 */
export default function WorkspaceEmpty() {
  const { t } = useTranslation()
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
      <p className="text-sm font-medium text-muted-foreground">{t('console.emptyTitle')}</p>
      <p className="text-xs text-muted-foreground/70">{t('console.emptyHint')}</p>
    </div>
  )
}
