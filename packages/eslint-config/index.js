//  共享 ESLint flat config：React + TypeScript 基线。
// 只承载与仓库无关的通用基线；app 语义规则（如 @/ 路径的 no-restricted-imports
// 弃用清单）留在各 app 的 eslint.config.js 叠加，避免共享层耦合 app 内部路径。
import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'

/** React + TS 基线（files 作用域 **\/*.{ts,tsx}），供 defineConfig extends 复用。 */
export const reactBase = {
  files: ['**/*.{ts,tsx}'],
  extends: [
    js.configs.recommended,
    tseslint.configs.recommended,
    reactHooks.configs.flat.recommended,
    reactRefresh.configs.vite,
  ],
  languageOptions: {
    globals: globals.browser,
  },
  rules: {
    // react-refresh 保持 error 以拦截新组件文件的非组件导出；
    // 组件文件随组件导出变体/常量的合法场景在各文件顶部按需豁免。
    'react-refresh/only-export-components': 'error',
  },
}

/**
 * 禁止代码里出现 FR / ADR / PR 编号（规则正文见 .claude/rules/no-ref-in-code.md）。
 *
 * 【为什么用 AST 规则而不是脚本扫文本】编号会写成多种变形（带连字符、紧贴、大小写混写），
 * 文本扫描要么漏、要么误伤注释里的文档路径。AST 选择器直接看「这个节点是不是
 * 标识符 / 字符串字面量 / 调用实参」，不用猜；而且与 Go 侧共用同一套语义
 * （Go 走 go/ast，见 scripts/check-no-ref-in-code.go）。
 *
 * 【为什么 PR 要额外限定】`PR` 在注释与标题里大量指 Pull Request（如「见 PR #74」），
 * 那不是编号。只拦「前缀与数字紧贴」的写法——那种形式只可能是编号。
 * 正则里的 (?<![A-Za-z0-9]) 是为了避开 XFR 这类前缀非编号的标识符。
 */
const REF_NUMBER_MESSAGE =
  '代码里禁止出现 FR / ADR / PR 编号（编号只属于文档）。请改用主题词命名；'
  + '若必须保留，加 eslint-disable-next-line 并注明原因。'

/**
 * 编号闸的 AST 选择器。
 * 前缀与数字之间允许分隔符（兼容带连字符与紧贴两种写法）；
 * PR 额外要求紧贴，以避开 Pull Request 语义。
 */
const REF_NUMBER_PATTERN = '(?<![A-Za-z0-9])(FR|ADR)[-_]?\\d{2,}|(?<![A-Za-z0-9])PR\\d{2,}'

/**
 * 生成一条 no-restricted-syntax 规则（带可选的额外豁免文件）。
 *
 * 【存量豁免机制】规则只约束新增与修改（见 .claude/rules/no-ref-in-code.md
 * 「执行范围：只约束新增与修改」）。ESLint 本身没有 diff 概念，所以采用官方
 * flat config 的 `ignores` 做文件级豁免：把「规则出台前已经带编号的文件」列出来，
 * 它们整体不受该规则约束。
 *
 * 这样做的代价是「存量文件里的新增编号也拦不住」。取舍理由：
 *   - 与 Go 侧的 --stock / --worktree 双模式对齐：存量由单独一轮清理，不在日常
 *     改动里顺手做（那会把功能提交搅成噪音）；
 *   - 若不做豁免，规则一上线就会让 CI 红 50 处存量错误，团队只能整体关掉规则；
 *   - 存量清单是有限且可缩的（清理掉一个就删一行），不会成为永久洞。
 *
 * @param {string[]} preexistingFiles 规则出台前已带编号的文件（相对 app 根）。
 * @returns {object} flat config 条目。
 */
export function makeNoRefInCodeConfig(preexistingFiles = []) {
  return {
    files: ['**/*.{ts,tsx,js,jsx,mjs,cjs}'],
    ...(preexistingFiles.length > 0 ? { ignores: preexistingFiles } : {}),
    rules: {
      'no-restricted-syntax': [
        'error',
        // 标识符：变量 / 函数 / 类 / 类型名
        {
          selector: `Identifier[name=/${REF_NUMBER_PATTERN}/i]`,
          message: REF_NUMBER_MESSAGE,
        },
        // 字符串字面量：测试标题、用例数据、环境变量名
        {
          selector: `Literal[value=/${REF_NUMBER_PATTERN}/i]`,
          message: REF_NUMBER_MESSAGE,
        },
        // 模板字符串
        {
          selector: `TemplateElement[value.raw=/${REF_NUMBER_PATTERN}/i]`,
          message: REF_NUMBER_MESSAGE,
        },
      ],
    },
  }
}

/** 无存量豁免的默认导出（新工程 / 已清干净的场景）。 */
export const noRefInCodeRules = makeNoRefInCodeConfig().rules

export default reactBase
