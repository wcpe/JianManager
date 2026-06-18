import { useTranslation } from 'react-i18next'
import { useThemeStore } from '@/stores/theme'
import { changeLanguage } from '@/i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

type Theme = 'light' | 'dark' | 'system'

/**
 * 系统设置页（FR-037）。
 * 将原侧栏底部的主题/语言开关沉淀为正式设置项，并预留后续设置分区。
 */
export default function SettingsPage() {
  const { t, i18n } = useTranslation()
  const { theme, setTheme } = useThemeStore()
  const currentLang = i18n.language as 'zh' | 'en'

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-2xl font-bold">{t('settings.title')}</h1>
        <p className="text-sm text-muted-foreground">{t('settings.subtitle')}</p>
      </div>

      {/* 外观：主题 + 语言 */}
      <Card>
        <CardHeader>
          <CardTitle>{t('settings.appearance')}</CardTitle>
          <CardDescription>{t('settings.appearanceDesc')}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center justify-between gap-4">
            <div>
              <p className="text-sm font-medium">{t('settings.theme')}</p>
              <p className="text-xs text-muted-foreground">{t('settings.themeDesc')}</p>
            </div>
            <Select value={theme} onValueChange={(v: string) => setTheme(v as Theme)}>
              <SelectTrigger size="sm" className="w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="light">{t('theme.light')}</SelectItem>
                <SelectItem value="dark">{t('theme.dark')}</SelectItem>
                <SelectItem value="system">{t('theme.system')}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="flex items-center justify-between gap-4">
            <div>
              <p className="text-sm font-medium">{t('settings.language')}</p>
              <p className="text-xs text-muted-foreground">{t('settings.languageDesc')}</p>
            </div>
            <Select value={currentLang} onValueChange={(v: string) => changeLanguage(v as 'zh' | 'en')}>
              <SelectTrigger size="sm" className="w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="zh">{t('language.zh')}</SelectItem>
                <SelectItem value="en">{t('language.en')}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </CardContent>
      </Card>

      {/* 后续设置占位 */}
      <Card>
        <CardHeader>
          <CardTitle>{t('settings.moreTitle')}</CardTitle>
          <CardDescription>{t('settings.moreDesc')}</CardDescription>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">{t('settings.comingSoon')}</p>
        </CardContent>
      </Card>
    </div>
  )
}
