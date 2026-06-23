import { useTranslation } from 'react-i18next'
import { Keyboard } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
} from '@/components/ui/dropdown-menu'

/**
 * 编辑器迷你 IDE 快捷键速查（FR-073）。
 *
 * 作为「快捷键全集」的可发现入口：在编辑器头部以下拉速查表呈现搜索/替换、撤销/重做、
 * 行操作、批量注释等键位。键位文案与 ide-extensions.ts 的实际绑定一致；说明文案走 i18n。
 * 仅展示信息、不持有编辑器状态，故可在任意编辑器头部复用。
 */
export default function EditorShortcutsHelp() {
  const { t } = useTranslation()

  /** 一行速查项：左侧能力说明，右侧键位（键位用 kbd 呈现，不翻译）。 */
  const rows: { label: string; keys: string }[] = [
    { label: t('editorIde.search'), keys: 'Ctrl+F' },
    { label: t('editorIde.replace'), keys: 'Ctrl+Alt+F' },
    { label: t('editorIde.undo'), keys: 'Ctrl+Z' },
    { label: t('editorIde.redo'), keys: 'Ctrl+Y / Ctrl+Shift+Z' },
    { label: t('editorIde.deleteLine'), keys: 'Ctrl+Shift+K' },
    { label: t('editorIde.copyLine'), keys: 'Ctrl+Shift+D' },
    { label: t('editorIde.moveLine'), keys: 'Alt+↑ / Alt+↓' },
    { label: t('editorIde.selectLine'), keys: 'Ctrl+L' },
    { label: t('editorIde.toggleComment'), keys: 'Ctrl+/' },
    { label: t('editorIde.toggleBlockComment'), keys: 'Ctrl+Shift+A' },
    { label: t('editorIde.save'), keys: 'Ctrl+S' },
  ]

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
              <kbd className="shrink-0 rounded border bg-muted px-1.5 py-0.5 font-mono text-[11px]">
                {r.keys}
              </kbd>
            </div>
          ))}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
