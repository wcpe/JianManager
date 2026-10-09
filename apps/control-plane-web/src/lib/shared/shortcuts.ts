export interface EditorShortcutRow {
  /** 左侧能力说明。 */
  label: string
  /** 右侧键位（键位用 kbd 呈现，不翻译）。 */
  keys: string
}

/** 生成快捷键速查行，集中锁住展示键位与 ide-extensions.ts 绑定的一致性。 */
export function editorShortcutRows(t: (key: string) => string): EditorShortcutRow[] {
  return [
    { label: t('editorIde.search'), keys: 'Ctrl+F' },
    { label: t('editorIde.replace'), keys: 'Ctrl+Alt+F' },
    { label: t('editorIde.undo'), keys: 'Ctrl+Z' },
    { label: t('editorIde.redo'), keys: 'Ctrl+Y / Ctrl+Shift+Z' },
    { label: t('editorIde.deleteLine'), keys: 'Ctrl+Shift+K' },
    { label: t('editorIde.copyLine'), keys: 'Ctrl+Shift+D' },
    { label: t('editorIde.moveLine'), keys: 'Alt+↑ / Alt+↓' },
    { label: t('editorIde.selectLine'), keys: 'Ctrl+L' },
    { label: t('editorIde.toggleComment'), keys: 'Ctrl+/' },
    { label: t('editorIde.toggleBlockComment'), keys: 'Shift+Alt+A' },
    { label: t('editorIde.save'), keys: 'Ctrl+S' },
  ]
}
