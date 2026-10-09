// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做平台配置取数/保存、客户端偏好（主题/语言）落盘与出站测试按钮接线。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { changeLanguage } from '@/i18n'
import { isPlatformAdmin } from '@/lib/roles'
import { useSettings, useUpdateSettings } from '@/api/settings'
import type { SettingCategory } from '@/pages/settings-form'
import { OutboundTestButton } from '@/components/OutboundTestButton'
import { SettingsPageView } from '@/components/views/settings/SettingsPageView'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 系统设置页容器（ADR-097 b 范式）：平台配置取数、保存 mutation、客户端偏好（主题/语言）落盘
 * 与两处出站连通性测试入口的接线都在这里决定，分类导航、分类面板与未保存拦截交共享视图。
 *
 * 受控状态归容器：当前分类 `category`——它是「面板在看哪一组配置」的筛选语义。
 * 编辑草稿、待确认切换与草稿键清理留在视图内（它们不随应用运行时变化）。
 * 主题与语言的值/变更经 props 注入，视图不 import 任何 store，也不自行切语言。
 * 平台配置分类仅平台管理员可见，角色判定在应用侧完成后注入。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function SettingsPage() {
  const { t, i18n } = useTranslation()
  const role = useAuthStore((s) => s.role)
  // 明暗偏好：只订阅本页要用的两个字段（主题色变化不牵动本页重渲染）。
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)
  const { data, isLoading, isError } = useSettings()
  const update = useUpdateSettings()

  /** 分类筛选：appearance 为客户端偏好，其余为平台配置分类。 */
  const [category, setCategory] = useState<SettingCategory>('appearance')

  return (
    <SettingsPageView
      isPlatformAdmin={isPlatformAdmin(role)}
      category={category}
      onCategoryChange={setCategory}
      data={data}
      isLoading={isLoading}
      isError={isError}
      saving={update.isPending}
      onSave={async (changed) => {
        try {
          await update.mutateAsync({ values: changed })
          toast.success(t('settings.saved', '已保存'))
          // 成功：视图据此清掉已落库的草稿键。
          return true
        } catch (err) {
          toast.error(errMessage(err, t('settings.saveFailed', '保存失败')))
          return false
        }
      }}
      theme={theme}
      onThemeChange={setTheme}
      language={i18n.language as 'zh' | 'en'}
      onLanguageChange={changeLanguage}
      renderOutboundTest={({ category: testCategory }) =>
        testCategory === 'network' ? (
          // 出站代理测试（FR-229）：目标可自定义（FR-280）。
          <OutboundTestButton
            defaultUrl="https://www.google.com"
            editable
            label={t('diagnostics.testProxy', '测试出站连通性')}
          />
        ) : (
          // JDK 下载源可达性测试（FR-229）：固定目标。
          <OutboundTestButton
            defaultUrl="https://api.foojay.io/disco/v3.0/distributions"
            label={t('diagnostics.testJdkSource', '测试 JDK 下载源')}
          />
        )
      }
    />
  )
}
