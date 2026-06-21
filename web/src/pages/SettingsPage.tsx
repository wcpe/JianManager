import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { toast } from 'sonner'
import { useThemeStore } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import { changeLanguage } from '@/i18n'
import { useSettings, useUpdateSettings, type SettingItem } from '@/api/settings'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

type Theme = 'light' | 'dark' | 'system'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/** 日志级别可选值，用于可编辑项的下拉。 */
const LOG_LEVELS = ['debug', 'info', 'warn', 'error'] as const

/** 取配置项的本地化标签，缺省回退键名本身。 */
function settingLabel(t: TFunction, key: string): string {
  return t(`settings.keys.${key}`, key)
}

/**
 * 系统设置页（FR-037 + FR-063）。
 * 外观/语言为客户端偏好（localStorage）；平台配置为服务端 DB 覆盖层，
 * 可编辑项落库即生效（覆盖 env/YAML），只读项展示当前生效值并提示需改配置重启。
 * 平台配置分组仅对平台管理员展示（后端 RBAC 同样收敛）。
 */
export default function SettingsPage() {
  const { t, i18n } = useTranslation()
  const { theme, setTheme } = useThemeStore()
  const role = useAuthStore((s) => s.role)
  const currentLang = i18n.language as 'zh' | 'en'
  const isPlatformAdmin = role === ROLE_PLATFORM_ADMIN

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

      {/* 平台配置：服务端 DB 覆盖层，仅平台管理员 */}
      {isPlatformAdmin && <PlatformConfigCard />}
    </div>
  )
}

/** 平台配置分组：可编辑项表单（保存调 PUT /settings）+ 只读项展示。 */
function PlatformConfigCard() {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useSettings()
  const update = useUpdateSettings()

  // 可编辑项的本地草稿：仅存用户改动；展示值回退到后端当前生效值（draft[key] ?? it.value）。
  const [draft, setDraft] = useState<Record<string, string>>({})

  const editable = data?.editable ?? []
  const readOnly = data?.readOnly ?? []

  // 计算被改动过的键（草稿值 != 后端当前值），只提交改动项。
  const changed: Record<string, string> = {}
  for (const it of editable) {
    if (draft[it.key] !== undefined && draft[it.key] !== it.value) changed[it.key] = draft[it.key]
  }
  const hasChanges = Object.keys(changed).length > 0

  const save = async () => {
    if (!hasChanges) return
    try {
      await update.mutateAsync({ values: changed })
      toast.success(t('settings.saved', '已保存'))
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('settings.saveFailed', '保存失败'))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('settings.platform')}</CardTitle>
        <CardDescription>{t('settings.platformDesc')}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading', '加载中…')}</p>}
        {isError && <p className="text-sm text-destructive">{t('settings.loadFailed', '加载平台配置失败')}</p>}

        {!isLoading && !isError && (
          <>
            {/* 可编辑项 */}
            <div className="space-y-3">
              <div className="flex items-center gap-2">
                <h3 className="text-sm font-semibold">{t('settings.editable', '可运行时调整')}</h3>
                <Badge variant="secondary">{editable.length}</Badge>
              </div>
              <p className="text-xs text-muted-foreground">{t('settings.editableHint', '保存后立即覆盖默认值。')}</p>

              <div className="divide-y rounded-md border">
                {editable.map((it) => (
                  <EditableRow
                    key={it.key}
                    item={it}
                    value={draft[it.key] ?? it.value}
                    onChange={(v) => setDraft((d) => ({ ...d, [it.key]: v }))}
                  />
                ))}
              </div>

              <div className="flex justify-end">
                <Button size="sm" onClick={save} disabled={!hasChanges || update.isPending}>
                  {update.isPending ? t('common.saving', '保存中…') : t('common.save', '保存')}
                </Button>
              </div>
            </div>

            {/* 只读项 */}
            <div className="space-y-3">
              <div className="flex items-center gap-2">
                <h3 className="text-sm font-semibold">{t('settings.readOnly', '只读（启动固定）')}</h3>
                <Badge variant="outline">{readOnly.length}</Badge>
              </div>
              <p className="text-xs text-muted-foreground">{t('settings.readOnlyHint', '如需修改请改配置文件/环境变量并重启。')}</p>

              <div className="divide-y rounded-md border">
                {readOnly.map((it) => (
                  <ReadOnlyRow key={it.key} item={it} />
                ))}
              </div>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

/** 单个可编辑配置项行：log.level 用下拉，其余用文本框；标注是否即时生效/已覆盖。 */
function EditableRow({
  item,
  value,
  onChange,
}: {
  item: SettingItem
  value: string
  onChange: (v: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-between gap-4 px-3 py-2">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <p className="text-sm font-medium truncate">{settingLabel(t, item.key)}</p>
          {item.overridden && <Badge variant="secondary">{t('settings.overridden', '已覆盖')}</Badge>}
          {!item.effectiveImmediately && (
            <Badge variant="outline">{t('settings.workerSide', 'Worker 侧生效')}</Badge>
          )}
        </div>
        <p className="text-xs text-muted-foreground font-mono">{item.key}</p>
      </div>
      {item.key === 'log.level' ? (
        <Select value={value} onValueChange={onChange}>
          <SelectTrigger size="sm" className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {LOG_LEVELS.map((lv) => (
              <SelectItem key={lv} value={lv}>
                {lv}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="h-8 w-56"
        />
      )}
    </div>
  )
}

/** 单个只读配置项行：展示当前生效值；敏感项标注「已脱敏」。 */
function ReadOnlyRow({ item }: { item: SettingItem }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-between gap-4 px-3 py-2">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <p className="text-sm font-medium truncate">{settingLabel(t, item.key)}</p>
          {item.sensitive && <Badge variant="outline">{t('settings.masked', '已脱敏')}</Badge>}
        </div>
        <p className="text-xs text-muted-foreground font-mono">{item.key}</p>
      </div>
      <code className="text-xs text-muted-foreground max-w-[14rem] truncate text-right">{item.value || '—'}</code>
    </div>
  )
}
