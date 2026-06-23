/**
 * 按文件类型推导注释符（FR-073 批量注释/取消注释）。纯函数，可单测。
 *
 * CodeMirror 的 toggleComment/toggleLineComment 通过 `EditorState.languageData` 里的
 * `commentTokens` 发现注释语法。yaml/json 的 StreamLanguage 与本项目自定义的
 * properties/toml 解析、以及纯文本兜底都不一定携带该数据，故由编辑器统一按
 * LanguageKind 注入，保证「批量注释/取消」对所有可编辑文件类型都生效且符合该格式惯例。
 */
import type { LanguageKind } from '../language'

/** 单行注释前缀 + 可选块注释包裹符（对齐 CodeMirror commentTokens 结构）。 */
export interface CommentTokens {
  /** 单行注释前缀（如 `#` / `//`），无单行注释语法时省略。 */
  line?: string
  /** 块注释起止符（如 HTML `<!-- -->`），无块注释语法时省略。 */
  block?: { open: string; close: string }
}

/**
 * 按语言种类返回注释符。
 * - yaml / properties / ini / toml / conf：`#`（脚本/配置惯例）；
 * - json（含 json5）：`//` + 块注释（JSON5 允许，普通 JSON 严格不允许但编辑器内便利优先，
 *   且本项目 json 文件多为 json5/带注释配置）；
 * - plain（log/txt/md/sh/...）：兜底 `#`（多数运行配置/脚本以 # 注释，最实用）。
 */
export function commentTokensFor(kind: LanguageKind): CommentTokens {
  switch (kind) {
    case 'json':
      return { line: '//', block: { open: '/*', close: '*/' } }
    case 'yaml':
    case 'properties':
    case 'toml':
    case 'plain':
    default:
      return { line: '#' }
  }
}

/**
 * 按文件名（经扩展名）推导注释符。HTML/XML 类按块注释 `<!-- -->`——
 * 这类文件不在 LanguageKind 的高亮分类内（统一 plain 高亮），但批量注释需用块注释符，
 * 故在文件名层单独识别，避免给 .xml/.html 误用 `#`。
 */
export function commentTokensForFilename(filename: string, kind: LanguageKind): CommentTokens {
  const ext = lowerExt(filename)
  if (ext === 'html' || ext === 'htm' || ext === 'xml' || ext === 'svg') {
    return { block: { open: '<!--', close: '-->' } }
  }
  if (ext === 'sql') {
    return { line: '--', block: { open: '/*', close: '*/' } }
  }
  if (ext === 'lua') {
    return { line: '--', block: { open: '--[[', close: ']]' } }
  }
  return commentTokensFor(kind)
}

/** 取小写扩展名（无扩展名返回空串）。与 paths.extName 同义，此处独立避免循环依赖耦合。 */
function lowerExt(filename: string): string {
  const base = filename.split(/[\\/]/).pop() ?? ''
  const dot = base.lastIndexOf('.')
  if (dot <= 0) return ''
  return base.slice(dot + 1).toLowerCase()
}
