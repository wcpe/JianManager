import { useTranslation } from 'react-i18next'
import { Plus } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { useDirectorStore } from '@/stores/director'
import type { WorkspacePreset } from '@/lib/workspace-preset'

/**
 * 「添加场景」菜单（FR-168）：从已保存的用户预设（FR-167 超级工作台另存的跨实例布局）导入为导播台场景。
 * 导入即克隆该预设卡片为新场景，追加到缩略图条末尾，并加入状态机（cold，切到才保活）。
 */
interface DirectorAddSceneMenuProps {
  /** 可选的用户预设（跨实例 + 单实例共享一份，FR-167）。 */
  userPresets: WorkspacePreset[]
  /** 触发按钮样式：default=轮廓小按钮；primary=空态主操作。 */
  variant?: 'default' | 'primary'
}

export default function DirectorAddSceneMenu({ userPresets, variant = 'default' }: DirectorAddSceneMenuProps) {
  const { t } = useTranslation()
  const addSceneFromPreset = useDirectorStore((s) => s.addSceneFromPreset)

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button size="sm" variant={variant === 'primary' ? 'default' : 'outline'}>
          <Plus className="size-4" />
          {t('director.addScene')}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[14rem]">
        {userPresets.length === 0 ? (
          <DropdownMenuItem disabled>{t('director.noPresets')}</DropdownMenuItem>
        ) : (
          userPresets.map((p) => (
            <DropdownMenuItem key={p.id} onClick={() => addSceneFromPreset(p)}>
              <span className="truncate">{p.name}</span>
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
