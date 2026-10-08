#!/usr/bin/env node
/**
 * 组件博物馆样例自检：受控组件的「开合」不能靠固定值。
 *
 * 背景——受控复合组件（ADR-097 b 范式）把开合交给外壳，视图自己不持 open：
 * 详情面板「由 `detailId`（容器按深链派生）决定，故本体不持 open」。样例若给它一个
 * **固定值**（`detailId={1}`）再配一个**空回调**（`onCloseDetail={() => {}}`），
 * 这个面板就弹出来关不掉——点关闭、按 Esc 都改不动它，因为能改变开合的只有那个常量。
 * views-ops 的群组详情正是如此，登记时被当成静态展示，直到有人点开才发现卡死。
 *
 * 判据（同一组件标签块内同时成立才算缺陷）：
 *   1. 有空的开合回调：`onCloseXxx={() => {}}` 或 `xxxOpenChange={() => {}}`
 *   2. 该块给某个开合值 prop 传了**字面量**（数字 / true / false / 引号串）
 * 传 `null` 是安全的——它表示「不打开」，弹窗根本不渲染；传标识符也安全——那说明外壳真的持有了状态。
 *
 * 用法：node scripts/check-museum-samples.mjs   （退出码非 0 即有缺陷）
 */

import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

const SECTIONS_DIR = 'apps/ui-museum/src/sections'

/**
 * 决定开合的 prop：传字面量就意味着开合被钉死。
 *
 * 只列**真正参与渲染条件**的键——判据是组件源码里出现 `xxx !== null &&` / `{xxx && (` / `open={xxx}`。
 * 不列 `activeGroupId` 这类「选中哪个」的数据值：它只决定渲染什么，不决定渲染与否，
 * 钉死它最多让内容不切换，不会把面板卡在屏幕上。
 */
const OPEN_PROP = /\b(detailId|activePanel|report|failures|failuresTaskId|open|visible|show[A-Z]\w*)=\{([^}]*)\}/
/** 空的开合回调：开合交给外壳，但外壳什么都没做。 */
const EMPTY_CLOSE_CB = /\b(onClose\w*|[a-z]\w*OpenChange)=\{\(\)\s*=>\s*\{\s*\}\}/
/** 值是不是字面量（标识符与 null 都不算）。 */
const LITERAL = /^\s*(?:\d+|true|false|'[^']*'|"[^"]*")\s*$/
/** 组件标签块的开头。 */
const TAG_OPEN = /<([A-Z]\w*)/

const files = readdirSync(SECTIONS_DIR).filter((f) => f.endsWith('.tsx'))
const problems = []

for (const file of files) {
  const lines = readFileSync(join(SECTIONS_DIR, file), 'utf8').split('\n')
  // 记录每个空回调所属的组件块范围：[标签名, 起始行, 回调行]
  let tagStart = -1
  let tagName = ''
  for (let i = 0; i < lines.length; i++) {
    const tag = TAG_OPEN.exec(lines[i])
    // 只在「顶层 JSX 标签」上重置：缩进对齐的 <Component 开头
    if (tag && /^\s*<[A-Z]/.test(lines[i])) {
      tagStart = i
      tagName = tag[1]
    }
    if (!EMPTY_CLOSE_CB.test(lines[i]) || tagStart < 0) continue

    // 在同一个组件块内找被钉死的开合值
    for (let k = tagStart; k <= i; k++) {
      const m = OPEN_PROP.exec(lines[k])
      if (!m) continue
      if (!LITERAL.test(m[2])) continue
      problems.push({
        file,
        line: i + 1,
        prop: EMPTY_CLOSE_CB.exec(lines[i])[1],
        tag: tagName,
        openProp: m[1],
        value: m[2].trim(),
        openLine: k + 1,
      })
      break
    }
  }
}

if (problems.length === 0) {
  console.log(`✓ 博物馆样例自检通过（${files.length} 个分区文件，无「固定开合值 + 空关闭回调」组合）`)
  process.exit(0)
}

console.error(`✗ 博物馆样例自检发现 ${problems.length} 处「弹出来关不掉」隐患：\n`)
for (const p of problems) {
  console.error(`  ${p.file}:${p.line}`)
  console.error(`    组件 <${p.tag}> 的 ${p.prop} 是空函数，但第 ${p.openLine} 行把 ${p.openProp}={${p.value}} 钉成了常量`)
  console.error(`    → 开合只由该常量决定，弹出来后点关闭/按 Esc 都改不动。改用 useState 代持外壳状态。\n`)
}
console.error('参见 apps/ui-museum/src/sections/views-ops.tsx 中 ViewsOps() 的样例状态写法。')
process.exit(1)
