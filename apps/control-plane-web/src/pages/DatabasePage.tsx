import { useTranslation } from 'react-i18next'
import { useAuthStore } from '@/stores/auth'
import DatabaseExplorer from '@/components/database/DatabaseExplorer'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/**
 * 数据库资源管理器页（FR-084）：平台管理员只读浏览 Control Plane 自身数据库。
 * 入口在侧栏「设置」组，仅平台管理员可见；本页再以角色兜底（非管理员显示无权限），
 * 后端 RBAC 同样收敛——三重把关守「数据库仅 Control Plane 读写」边界（本能力只读）。
 */
export default function DatabasePage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isPlatformAdmin = role === ROLE_PLATFORM_ADMIN

  if (!isPlatformAdmin) {
    return (
      <div className="grid h-full place-items-center text-sm text-muted-foreground">
        {t('database.forbidden')}
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-4">
      <div className="shrink-0">
        <h1 className="text-xl font-bold">{t('database.title')}</h1>
        <p className="text-xs text-muted-foreground">{t('database.subtitle')}</p>
      </div>
      <DatabaseExplorer />
    </div>
  )
}
