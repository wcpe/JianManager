import { useTranslation } from 'react-i18next'
import { LogOut, UserRound } from 'lucide-react'

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { ROLE_LABEL_KEY } from '@/lib/shared/roles'

export interface AccountMenuProps {
  /** 当前用户名（未加载传 null）。 */
  username: string | null
  /** 当前角色值（null 表示未知，不显示角色行）。 */
  role: number | null
  /** 退出登录。 */
  onLogout: () => void
}

/**
 * 账户菜单（FR-162）：显示用户名 / 角色 + 退出登录（接管 FR-132 的退出图标化）。
 *
 * 受控视图（ADR-097 c 范式）：账户态与退出动作由外壳从 auth store 读出后注入。
 * 角色文案复用包内 `roles` 的 `ROLE_LABEL_KEY`（与用户管理页同源，避免重复维护）。
 */
export function AccountMenu({ username, role, onLogout }: AccountMenuProps) {
  const { t } = useTranslation()
  const roleKey = role != null ? ROLE_LABEL_KEY[role] : undefined

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('header.account')}
          className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <span className="grid size-6 shrink-0 place-items-center rounded-full bg-primary/15 text-primary">
            <UserRound className="size-3.5" />
          </span>
          <span className="hidden max-w-32 truncate text-xs font-medium text-foreground sm:block">
            {username ?? t('header.account')}
          </span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <div className="px-2 py-1.5">
          <p className="truncate text-sm font-medium">{username ?? '—'}</p>
          {roleKey && <p className="text-xs text-muted-foreground">{t(roleKey)}</p>}
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onClick={onLogout}>
          <LogOut className="size-4" />
          {t('common.logout')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
