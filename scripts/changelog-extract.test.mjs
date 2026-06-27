// changelog-extract.mjs 纯解析逻辑单测（FR-173）。
// 运行：node --test scripts/changelog-extract.test.mjs
//
// 只测纯函数 extractSection（不跑 CLI / 不读真文件），覆盖：
//   - 提取 [Unreleased] 段正文
//   - 提取指定版本段正文（含全角括号日期头 `## X.Y.Z（…）`）
//   - 缺失版本 → 抛错（让 CI 失败而非发空说明）
//   - 空段 → 抛错

import { test } from 'node:test'
import assert from 'node:assert/strict'

import { extractSection } from './changelog-extract.mjs'

const SAMPLE = `# CHANGELOG

> 说明行，不属于任何段。

---

## [Unreleased]

### 新增
- 未发布的新功能 A
- 未发布的新功能 B

### 修复
- 未发布的修复 C

## 0.11.0（2026-07-01）

### 新增
- 0.11.0 的功能 X

### 变更
- 0.11.0 的变更 Y

## 0.10.0（2026-06-27）

### 新增
- 0.10.0 的功能 Z
`

test('extractSection: --unreleased 提取 [Unreleased] 段正文', () => {
  const body = extractSection(SAMPLE, { unreleased: true })
  assert.match(body, /未发布的新功能 A/)
  assert.match(body, /未发布的修复 C/)
  // 不得越界吃进下一个版本段
  assert.doesNotMatch(body, /0\.11\.0 的功能 X/)
  // 不得包含段标题自身
  assert.doesNotMatch(body, /## \[Unreleased\]/)
})

test('extractSection: --version 0.11.0 提取该版本段正文（全角括号头）', () => {
  const body = extractSection(SAMPLE, { version: '0.11.0' })
  assert.match(body, /0\.11\.0 的功能 X/)
  assert.match(body, /0\.11\.0 的变更 Y/)
  // 不得越界吃进 0.10.0 段或往上吃 [Unreleased]
  assert.doesNotMatch(body, /0\.10\.0 的功能 Z/)
  assert.doesNotMatch(body, /未发布的新功能 A/)
})

test('extractSection: --version 0.10.0 提取末尾版本段（文件结尾收口）', () => {
  const body = extractSection(SAMPLE, { version: '0.10.0' })
  assert.match(body, /0\.10\.0 的功能 Z/)
  assert.doesNotMatch(body, /0\.11\.0 的功能 X/)
})

test('extractSection: 带 v 前缀的版本号也能匹配（v0.11.0 == 0.11.0）', () => {
  const body = extractSection(SAMPLE, { version: 'v0.11.0' })
  assert.match(body, /0\.11\.0 的功能 X/)
})

test('extractSection: 缺失版本抛错', () => {
  assert.throws(() => extractSection(SAMPLE, { version: '9.9.9' }), /9\.9\.9/)
})

test('extractSection: 空段抛错（让 CI 失败而非发空说明）', () => {
  const emptyUnreleased = `# CHANGELOG

## [Unreleased]

## 0.10.0（2026-06-27）

### 新增
- 有内容
`
  assert.throws(() => extractSection(emptyUnreleased, { unreleased: true }), /空|empty|Unreleased/i)
})

test('extractSection: 仅含空白的段也视为空并抛错', () => {
  const whitespaceOnly = `# CHANGELOG

## 0.12.0（2026-08-01）


## 0.11.0（2026-07-01）

### 新增
- x
`
  assert.throws(() => extractSection(whitespaceOnly, { version: '0.12.0' }), /空|empty/i)
})
