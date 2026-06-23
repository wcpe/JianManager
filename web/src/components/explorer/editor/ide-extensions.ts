/**
 * 编辑器迷你 IDE 增强扩展集（FR-073）。
 *
 * 在 FR-070 共享 CodeEditor 之上叠加：搜索/替换面板、撤销/重做之外的行操作
 * （删除一行/复制一行/上下移动一行/选中整行）、按文件类型的批量注释/取消注释，
 * 以及与既有 Ctrl+S 历史保存不冲突的快捷键全集。
 *
 * 抽为独立工厂便于：CodeEditor 组装时一行接入；keymap 配置抽纯函数可单测
 * （断言无键位与 Ctrl+S 冲突、命令绑定正确）。
 */
import type { Extension } from '@codemirror/state'
import { EditorState } from '@codemirror/state'
import { type KeyBinding, keymap } from '@codemirror/view'
import {
  copyLineDown,
  copyLineUp,
  deleteLine,
  moveLineDown,
  moveLineUp,
  selectLine,
  toggleComment,
  toggleBlockComment,
} from '@codemirror/commands'
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search'
import { languageKindFor } from '../language'
import { commentTokensForFilename, type CommentTokens } from './comment'

/**
 * FR-073 行操作与注释快捷键。
 *
 * 键位选择避开 Ctrl+S（保存，FR-070）及 CodeMirror 默认/历史键位：
 * - 复制一行 Shift-Mod-d（向下）；删除一行 Shift-Mod-k；上下移动一行 Alt-↑/↓（CM 默认即此，显式列出便于自测断言）；
 * - 选中整行 Mod-l；批量注释/取消 Mod-/（行注释）与 Shift-Alt-a（块注释），与主流 IDE 一致。
 *
 * 纯函数：不依赖 DOM/视图，返回静态绑定数组，供单测断言「键位集合不含 Mod-s、命令引用正确」。
 */
export function ideKeymap(): readonly KeyBinding[] {
  return [
    { key: 'Mod-/', run: toggleComment },
    { key: 'Shift-Alt-a', run: toggleBlockComment },
    { key: 'Shift-Mod-k', run: deleteLine },
    { key: 'Shift-Mod-d', run: copyLineDown },
    { key: 'Shift-Mod-Alt-d', run: copyLineUp },
    { key: 'Alt-ArrowDown', run: moveLineDown },
    { key: 'Alt-ArrowUp', run: moveLineUp },
    { key: 'Mod-l', run: selectLine },
  ]
}

/**
 * 把注释符喂给 CodeMirror 的 languageData，使 toggleComment/toggleBlockComment
 * 对所有可编辑文件类型（含纯文本/自定义 StreamLanguage）都能按该格式惯例工作。
 * 同时返回的 commentTokens 形状即 CM 约定（line / block:{open,close}）。
 */
function commentLanguageData(filename: string): Extension {
  const tokens: CommentTokens = commentTokensForFilename(filename, languageKindFor(filename))
  const commentTokens: { line?: string; block?: { open: string; close: string } } = {}
  if (tokens.line) commentTokens.line = tokens.line
  if (tokens.block) commentTokens.block = tokens.block
  return EditorState.languageData.of(() => [{ commentTokens }])
}

/**
 * 组装 FR-073 增强扩展。按 filename 注入对应注释符；启用搜索/替换面板
 * （顶部、保留默认搜索键 Mod-f / 替换 Mod-Alt-f，支持正则/大小写/全词由面板勾选，
 * 全部替换由面板「replace all」）。行操作/注释键位以高优先级置于默认键位之前。
 */
export function ideExtensions(filename: string): Extension[] {
  return [
    commentLanguageData(filename),
    search({ top: true }),
    highlightSelectionMatches(),
    keymap.of([...ideKeymap(), ...searchKeymap]),
  ]
}
