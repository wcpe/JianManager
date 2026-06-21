import type { Extension } from '@codemirror/state'
import { StreamLanguage } from '@codemirror/language'
import { json } from '@codemirror/lang-json'
import { yaml } from '@codemirror/lang-yaml'
import { extName } from './paths'

/**
 * 编辑器语言种类（FR-070 多格式高亮）。
 * yaml/json 用专用语言包；properties/ini/toml 用轻量 StreamLanguage（无新依赖）；
 * 其余（log/txt/md/sh/...）用纯文本 + 通用高亮兜底。
 */
export type LanguageKind = 'yaml' | 'json' | 'properties' | 'toml' | 'plain'

/**
 * 按文件名后缀分类语言种类（纯函数，可单测）。
 * 无后缀的常见运行日志名（如 latest.log 已含后缀）走后缀；纯文本兜底为 plain。
 */
export function languageKindFor(filename: string): LanguageKind {
  const ext = extName(filename)
  switch (ext) {
    case 'yml':
    case 'yaml':
      return 'yaml'
    case 'json':
    case 'json5':
      return 'json'
    case 'properties':
    case 'ini':
    case 'cfg':
    case 'conf':
      return 'properties'
    case 'toml':
      return 'toml'
    default:
      return 'plain'
  }
}

/** key=value / key: value + # 或 ! 注释的轻量流式解析（.properties/.ini/.cfg/.conf）。 */
const propertiesStream = StreamLanguage.define<{ afterKey: boolean }>({
  startState: () => ({ afterKey: false }),
  token(stream, state) {
    if (stream.sol()) state.afterKey = false
    if (stream.eatSpace()) return null
    const ch = stream.peek()
    if (ch === '#' || ch === '!' || (ch === ';' && stream.sol())) {
      stream.skipToEnd()
      return 'comment'
    }
    if (!state.afterKey) {
      // 段标题 [section]
      if (ch === '[') {
        stream.skipToEnd()
        return 'heading'
      }
      if (stream.eatWhile(/[^=:\s]/)) {
        return 'propertyName'
      }
    }
    if (ch === '=' || ch === ':') {
      stream.next()
      state.afterKey = true
      return 'operator'
    }
    stream.skipToEnd()
    return 'string'
  },
})

/** [section] / key = value / # 注释 的轻量 TOML 流式解析。 */
const tomlStream = StreamLanguage.define<{ afterKey: boolean }>({
  startState: () => ({ afterKey: false }),
  token(stream, state) {
    if (stream.sol()) state.afterKey = false
    if (stream.eatSpace()) return null
    const ch = stream.peek()
    if (ch === '#') {
      stream.skipToEnd()
      return 'comment'
    }
    if (ch === '[') {
      stream.skipToEnd()
      return 'heading'
    }
    if (ch === '"' || ch === "'") {
      const quote = stream.next()
      while (!stream.eol()) {
        if (stream.next() === quote) break
      }
      return 'string'
    }
    if (!state.afterKey && stream.eatWhile(/[A-Za-z0-9_.-]/)) {
      return 'propertyName'
    }
    if (ch === '=') {
      stream.next()
      state.afterKey = true
      return 'operator'
    }
    if (stream.eatWhile(/[0-9.+\-eE]/)) {
      return 'number'
    }
    stream.next()
    return null
  },
})

/** 按文件名返回 CodeMirror 语言扩展（FR-070）。plain 返回空数组（纯文本 + 默认高亮兜底）。 */
export function languageExtensionFor(filename: string): Extension[] {
  switch (languageKindFor(filename)) {
    case 'yaml':
      return [yaml()]
    case 'json':
      return [json()]
    case 'properties':
      return [propertiesStream]
    case 'toml':
      return [tomlStream]
    default:
      return []
  }
}
