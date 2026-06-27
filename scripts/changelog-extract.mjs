// CHANGELOG 段落提取（FR-173 发布管线用，见 ADR-036 §3）。
//
// 用途：发布管线据触发类型提取 CHANGELOG.md 的一段正文作为 GitHub Release 说明（body）。
//   - 滚动预发布（push master）：取 `## [Unreleased]` 段正文。
//   - 正式发布（push tag vX.Y.Z）：取 `## X.Y.Z（…）` 版本段正文。
//
// 用法：
//   node scripts/changelog-extract.mjs --unreleased        # 输出 [Unreleased] 段到 stdout
//   node scripts/changelog-extract.mjs --version 0.11.0     # 输出指定版本段（v 前缀可有可无）
//   node scripts/changelog-extract.mjs --version 0.11.0 --file path/to/CHANGELOG.md
//
// 找不到段 / 段为空 → 打印错误到 stderr 并以非零退出，让 CI 失败（而非发空说明）。
//
// 与 gen-licenses.mjs 同栈（纯 Node ESM，零运行时依赖）。纯解析逻辑 extractSection 已导出，
// 供 changelog-extract.test.mjs 单测（node --test）。

import { readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(SCRIPT_DIR, '..')

/** 段标题行：以 `## ` 起始（二级标题）。一级 `# ` 与三级 `### ` 不算段边界。 */
const SECTION_HEADER_RE = /^##\s+(.+?)\s*$/

/** 去掉版本号可选的前导 `v`/`V`，便于 `v0.11.0` 与 `0.11.0` 等价匹配。 */
function normalizeVersion(v) {
  return String(v).trim().replace(/^v/i, '')
}

/**
 * 判断某个 `## ` 段标题是否匹配目标。
 * @param {string} headerText 段标题去掉 `## ` 后的文本，如 `[Unreleased]` 或 `0.11.0（2026-07-01）`
 * @param {{unreleased?: boolean, version?: string}} target
 */
function headerMatches(headerText, target) {
  if (target.unreleased) {
    // 容忍 `[Unreleased]` 周围空白；大小写不敏感。
    return /^\[unreleased\]$/i.test(headerText.trim())
  }
  const want = normalizeVersion(target.version)
  // 版本段标题形如 `0.11.0（2026-07-01）` 或 `0.11.0`：取其前导版本号 token 比对，
  // 避免被日期 / 全角括号干扰。token = 开头的「数字.数字...（含预发布后缀）」连续段。
  const m = headerText.trim().match(/^v?([0-9][0-9A-Za-z.+-]*)/)
  if (!m) return false
  return normalizeVersion(m[1]) === want
}

/**
 * 从 CHANGELOG 全文提取目标段的正文（不含段标题自身）。
 *
 * 段边界：从匹配的 `## ` 标题的**下一行**起，到下一个 `## ` 标题（或文件结尾）止；
 * 正文首尾空行裁剪。找不到段或正文为空（仅空白）→ 抛 Error（调用方据此非零退出）。
 *
 * @param {string} text CHANGELOG.md 全文
 * @param {{unreleased?: boolean, version?: string}} target
 * @returns {string} 段正文（已 trim）
 */
export function extractSection(text, target) {
  const lines = String(text).split(/\r?\n/)
  let start = -1
  for (let i = 0; i < lines.length; i++) {
    const m = lines[i].match(SECTION_HEADER_RE)
    if (m && headerMatches(m[1], target)) {
      start = i + 1
      break
    }
  }
  const label = target.unreleased ? '[Unreleased]' : normalizeVersion(target.version)
  if (start === -1) {
    throw new Error(`CHANGELOG 中找不到段「${label}」`)
  }
  let end = lines.length
  for (let i = start; i < lines.length; i++) {
    if (SECTION_HEADER_RE.test(lines[i])) {
      end = i
      break
    }
  }
  const body = lines.slice(start, end).join('\n').trim()
  if (body === '') {
    throw new Error(`CHANGELOG 段「${label}」为空，拒绝发布空说明`)
  }
  return body
}

/** 解析命令行参数为 { unreleased?, version?, file? }。 */
function parseArgs(argv) {
  const opts = {}
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--unreleased') {
      opts.unreleased = true
    } else if (a === '--version') {
      opts.version = argv[++i]
    } else if (a.startsWith('--version=')) {
      opts.version = a.slice('--version='.length)
    } else if (a === '--file') {
      opts.file = argv[++i]
    } else if (a.startsWith('--file=')) {
      opts.file = a.slice('--file='.length)
    }
  }
  return opts
}

function main() {
  const opts = parseArgs(process.argv.slice(2))
  if (!opts.unreleased && !opts.version) {
    process.stderr.write('用法：changelog-extract.mjs (--unreleased | --version X.Y.Z) [--file CHANGELOG.md]\n')
    process.exit(2)
  }
  const file = opts.file ? resolve(opts.file) : join(REPO_ROOT, 'CHANGELOG.md')
  let text
  try {
    text = readFileSync(file, 'utf8')
  } catch (e) {
    process.stderr.write(`读取 ${file} 失败：${e.message}\n`)
    process.exit(1)
  }
  try {
    const body = extractSection(text, opts)
    process.stdout.write(body + '\n')
  } catch (e) {
    process.stderr.write(`${e.message}\n`)
    process.exit(1)
  }
}

// 仅在作为脚本直接运行时执行 CLI（被 import 作单测时不跑）。
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main()
}
