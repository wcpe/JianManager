import { useTranslation } from 'react-i18next'
import { Keyboard } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Kbd } from '@jianmanager/ui/components/kbd'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
} from '@jianmanager/ui/components/dropdown-menu'
import { editorShortcutRows } from '@/lib/shared/shortcuts'

/** 
 * 编辑器迷你 IDE 快捷键速查。
 *
 * 作为「快捷键全集」的可发现入口：在编辑器头部以下拉速查表呈现搜索/替换、撤销/重做、
 * 行操作、批量注释等键位。键位文案与 ide-extensions.ts 的实际绑定一致；说明文案走 i18n。
 * 仅展示信息、不持有编辑器状态，故可在任意编辑器头部复用。
 */
export default function EditorShortcutsHelp() {
  const { t } = useTranslation()
  const rows = editorShortcutRows(t)

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          size="sm"
          variant="ghost"
          className="h-7 gap-1 px-2 text-xs"
          title={t('editorIde.shortcuts')}
        >
          <Keyboard className="size-3.5" /> {t('editorIde.shortcuts')}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <div className="px-2 py-1.5 text-xs font-medium text-muted-foreground">
          {t('editorIde.shortcuts')}
        </div>
        <div className="max-h-80 overflow-auto px-1 pb-1">
          {rows.map((r) => (
            <div
              key={r.keys}
              className="flex items-center justify-between gap-3 rounded px-2 py-1 text-xs"
            >
              <span className="truncate">{r.label}</span>
              {/* 快捷键组合用等宽字体更易分辨，其余外观由 Kbd 统一。 */}
              <Kbd className="border font-mono">{r.keys}</Kbd>
            </div>
          ))}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
