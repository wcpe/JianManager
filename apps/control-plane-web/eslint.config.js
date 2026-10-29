import { defineConfig, globalIgnores } from 'eslint/config'
import { reactBase } from '@jianmanager/eslint-config'

/**
 * 禁止直接用原生 `<select>` 的回潮闸（本轮 UI 组件收敛）。
 *
 * 判定方式用 `no-restricted-syntax` 的 JSXOpeningElement 选择器而不是 `react/forbid-elements`：
 * 后者需要额外插件（本仓未装），而 ESLint 原生选择器足以表达「JSX 里出现名为 select 的元素」，
 * 且**只覆盖 JSX 形态**——`document.createElement('select')` 这类命令式用法不在其管辖内
 * （仓库里目前没有这种用法，真出现时由 review 把关，不值得为它引入 AST 遍历依赖）。
 *
 * 【豁免方式】确实需要原生 select 语义时（例如需要 `form` 原生提交、移动端系统选择器、
 * 或候选数量过大不适合 Radix 全量挂载），二选一：
 *   1. **首选**：改用组件库的 `NativeSelect`（`@jianmanager/ui/components/native-select`）——
 *      它就是「原生 select + 统一外观」，既保住原生语义，又不触发本规则；
 *   2. 若确实不能包组件库（如极特殊的自定义渲染），在该行加
 *      `// eslint-disable-next-line no-restricted-syntax -- <一句话说明为什么必须用原生>`
 *      并在注释里写清原因；无原因的禁用会在 review 时被打回。
 */
const NO_RAW_SELECT = {
  selector: "JSXOpeningElement[name.name='select']",
  message:
    '禁止直接使用原生 <select>：改用 @jianmanager/ui/components/select 的 Select（固定小枚举）或 native-select 的 NativeSelect（保留原生语义但统一外观）。确需原生写法请加 eslint-disable 并注明原因。',
}

/**
 * 禁止直接用原生 `<textarea>`：改用 `@jianmanager/ui/components/textarea`。
 *
 * 与 select 规则同理，只是没有「必须保留原生」的强需求场景——
 * textarea 的样式收敛（圆角/边框色/焦点环/禁用态）本来就是纯视觉问题，
 * 没有 form 原生提交之类的硬约束。
 */
const NO_RAW_TEXTAREA = {
  selector: "JSXOpeningElement[name.name='textarea']",
  message:
    '禁止直接使用原生 <textarea>：改用 @jianmanager/ui/components/textarea 的 Textarea（已统一圆角、边框色、焦点环与禁用态）。确需原生写法请加 eslint-disable 并注明原因。',
}

/**
 * 禁止直接用原生 `<table>` / `<thead>` / `<tbody>` / `<tr>` / `<td>` / `<th>`：
 * 改用 `@jianmanager/ui/components/table` 的对应组件。
 *
 * 【为什么整组禁】只禁 `<table>` 不够——有人可以绕过外层用 `<div role="table">`
 * 或直接写 `<tr>`，那样样式同样会漂移。六个标签成组禁止，才能保证表格的
 * 边框、行悬停、单元格内距都走同一套取值。
 *
 * 【豁免】虚拟化表格（用 `div` + 行内 `height` 自己算行的场景）不受本规则影响，
 * 因为它本来就不写 `<table>`；若确有必须用原生表格结构的特殊场景，
 * 加 `eslint-disable-next-line no-restricted-syntax -- 原因`。
 */
const NO_RAW_TABLE = {
  selector:
    "JSXOpeningElement[name.name=/^(table|thead|tbody|tfoot|tr|td|th)$/]",
  message:
    '禁止直接使用原生 table 标签：改用 @jianmanager/ui/components/table 的 Table / TableHeader / TableBody / TableRow / TableHead / TableCell。确需原生写法请加 eslint-disable 并注明原因。',
}

/**
 * 【为什么不禁原生 `<button>`】213 处原生 button 里，有相当一部分是
 * **按规范就该保留原生**的：树节点（需精确控制 `role="treeitem"` / `aria-level` /
 * `aria-expanded`）、第三方库要求的目标元素（Radix asChild）、依赖原生拖拽事件的元素。
 * GitHub Primer 的做法也是让 `ActionList.Item` 独立成组件、而不是把一切塞进 Button。
 *
 * 全面禁止会逼着这些场景去写大量 `eslint-disable`，反而让豁免注释失去警示作用。
 * 因此 button 靠「组件库提供 `variant="invisible"` + `size="auto"`」引导收敛，
 * 不做 lint 强制。新增按钮时优先用 `Button`，只有确属上述三类才写原生。
 */

export default defineConfig([
  // .tmp/ 是过程性暂存目录（探针脚本、中间产物），本就被 .gitignore 忽略；
  // 它不该让 lint 变红——否则每个临时脚本都会污染仓库级 lint 结果。
  // coverage/ 是覆盖率报告产物。
  globalIgnores(['dist', 'coverage', '.tmp', 'public/mockServiceWorker.js']),
  //  共享 React+TS 基线（抽 @jianmanager/eslint-config）作顶层条目
  //（含内部 extends，不能再被 extends 嵌套）；下方同 files 作用域条目为 app 语义叠加。
  reactBase,
  {
    files: ['**/*.{ts,tsx}'],
    rules: {
      //  弃用 shadcn Card 松散用法：阻断业务代码新引入，统一走 Panel / StatCard。
      // @/ 路径为 app 内部语义，故留 app 层不入共享包。
      //
      // 【card 的阻断已移除】原规则拦的是 `@/components/ui/card`——那是 ADR-103 回迁前的
      //  应用内旧路径，如今已不存在。`Card` 本身在包内恢复启用（的弃用撤销，
      // 理由见 packages/ui/src/components/card.tsx 文件头）：它是「最小白盒」，
      // 而 Panel（三段式）与 StatCard（KPI）都不具备该形态，弃用只会让裸容器需求散落成手写样式。
      'no-restricted-imports': [
        'error',
        {
          paths: [],
        },
      ],
    },
  },
  {
    // 只对应用源码生效：e2e（Playwright 的选择器字符串里可能出现 '<select'）与测试夹具不在此列。
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/**/*.test.{ts,tsx}', 'src/**/*.dom.test.{ts,tsx}'],
    rules: {
      'no-restricted-syntax': ['error', NO_RAW_SELECT, NO_RAW_TEXTAREA, NO_RAW_TABLE],
    },
  },
])
