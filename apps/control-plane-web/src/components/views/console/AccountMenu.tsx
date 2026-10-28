import { useTranslation } from 'react-i18next'
import { LogOut, UserRound } from 'lucide-react'

import { Avatar, AvatarFallback } from '@jianmanager/ui/components/avatar'
import { Button } from '@jianmanager/ui/components/button'
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
 * 受控视图（ADR-103 c 范式）：账户态与退出动作由外壳从 auth store 读出后注入。
 * 角色文案复用包内 `roles` 的 `ROLE_LABEL_KEY`（与用户管理页同源，避免重复维护）。
 */
export function AccountMenu({ username, role, onLogout }: AccountMenuProps) {
  const { t } = useTranslation()
  const roleKey = role != null ? ROLE_LABEL_KEY[role] : undefined

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        {/*
          账户入口：图标 + 用户名组成的紧凑控件，没有按钮边框与底色，
          因此走 invisible + auto（不自带任何外壳），外观全部由下方 className 决定。
          `justify-start` 抵消 Button 基础类的居中，让头像始终贴左。
        */}
        <Button
          type="button"
          variant="invisible"
          size="auto"
          aria-label={t('header.account')}
          className="flex items-center justify-start gap-1.5 rounded-md px-1.5 py-1 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          {/*
            头像位改用组件库 Avatar：主控台没有图片头像源（后端不存用户头像），
            但「图片加载失败自动回落 Fallback」正是纯图标/首字母头像的正确承载形态——
            以后接上头像上传只需在 Fallback 之前插一个 <AvatarImage>，布局与尺寸钩子不用动。
            size="sm" 是 24px，与此前手写的 `size-6` 一致，故布局零位移。
          */}
          <Avatar size="sm">
            <AvatarFallback className="bg-primary/15 text-primary">
              <UserRound className="size-3.5" />
            </AvatarFallback>
          </Avatar>
          <span className="hidden max-w-32 truncate text-xs font-medium text-foreground sm:block">
            {username ?? t('header.account')}
          </span>
        </Button>
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
