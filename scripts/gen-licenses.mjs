// 构建期依赖与许可证扫描（FR-135）：全覆盖三源——web(npm) + bot-worker(npm) + Go(go.mod)，
// 产出 web/public/licenses.json 供前端 /licenses 页读取。**构建期生成、不手维护**。
//
// 用法：node scripts/gen-licenses.mjs            （由 Makefile `gen-licenses` 调用，build 前置）
//
// npm：license-checker-rseidelsohn（web devDependency）分别按 --production / --development 扫，
//      运行时/开发分区即据此；作者取 publisher、链接取 repository、全文读 licenseFile。
// Go ：go-licenses csv（实际链接进二进制的包集，比 go.mod 全图更贴近「真正分发」），
//      版本/目录/全文用 go list -m 补全；go-licenses 不可用时回退 go list + 许可证启发式识别。
//
// 设计为构建期稳健：任一源失败只告警并跳过该源（产出可能为部分），不让整个 build 崩。

import { execSync } from 'node:child_process'
import { existsSync, readFileSync, readdirSync, writeFileSync, mkdirSync, statSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(SCRIPT_DIR, '..')
const WEB_DIR = join(REPO_ROOT, 'web')
const BOT_DIR = join(REPO_ROOT, 'bot-worker')
const OUT_FILE = join(WEB_DIR, 'public', 'licenses.json')

const MAX_BUFFER = 128 * 1024 * 1024
const MAX_LICENSE_TEXT = 64 * 1024
const LICENSE_FILE_RE = /^(LICENSE|LICENCE|COPYING|COPYRIGHT)(\.[\w.-]+)?$/i

/** 读许可证全文（截断到上限），失败返回空串。 */
function readLicenseText(file) {
  try {
    if (!file || !existsSync(file)) return ''
    return readFileSync(file, 'utf8').slice(0, MAX_LICENSE_TEXT)
  } catch {
    return ''
  }
}

/** 在模块目录里找许可证文件并读取全文。 */
function readLicenseFromDir(dir) {
  try {
    const hit = readdirSync(dir).find((f) => LICENSE_FILE_RE.test(f) && statSync(join(dir, f)).isFile())
    return hit ? readLicenseText(join(dir, hit)) : ''
  } catch {
    return ''
  }
}

/** 极简 SPDX 启发式识别（仅 go-licenses 不可用时兜底用）。 */
function detectLicense(text) {
  const t = text.slice(0, 4000)
  if (/Apache License,?\s+Version 2\.0/i.test(t)) return 'Apache-2.0'
  if (/MIT License/i.test(t) || /Permission is hereby granted, free of charge/i.test(t)) return 'MIT'
  if (/Mozilla Public License Version 2\.0/i.test(t)) return 'MPL-2.0'
  if (/GNU GENERAL PUBLIC LICENSE\s+Version 3/i.test(t)) return 'GPL-3.0'
  if (/GNU LESSER GENERAL PUBLIC LICENSE/i.test(t)) return 'LGPL'
  if (/Redistribution and use in source and binary forms/i.test(t)) {
    if (/Neither the name/i.test(t)) return 'BSD-3-Clause'
    return 'BSD-2-Clause'
  }
  if (/The ISC License/i.test(t) || /ISC License/i.test(t)) return 'ISC'
  return 'Unknown'
}

/** licenses 字段可能是字符串或数组，归一为 ` / ` 连接的字符串。 */
function normLicense(lic) {
  if (Array.isArray(lic)) return lic.join(' / ')
  return String(lic || 'Unknown')
}

// ─────────────────────────── npm（web / bot-worker） ───────────────────────────

/** 跑一次 license-checker，返回 { "name@version": meta } 映射；失败抛出。 */
function runLicenseChecker(targetDir, mode /* 'production' | 'development' */) {
  const cmd = `npx --yes license-checker-rseidelsohn --json --${mode} --start "${targetDir}"`
  const out = execSync(cmd, { cwd: WEB_DIR, maxBuffer: MAX_BUFFER, stdio: ['ignore', 'pipe', 'ignore'] }).toString()
  const start = out.indexOf('{')
  const end = out.lastIndexOf('}')
  if (start < 0 || end < 0) return {}
  return JSON.parse(out.slice(start, end + 1))
}

/** 扫一个 npm 源（web/bot-worker），返回依赖数组。 */
function scanNpm(scope, dir) {
  if (!existsSync(join(dir, 'node_modules'))) {
    console.warn(`[gen-licenses] 跳过 ${scope}：未安装依赖（${join(dir, 'node_modules')} 不存在）`)
    return []
  }
  // 自身包名（排除根包，license-checker 会把被扫包自身也列出）。
  let selfName = ''
  try {
    selfName = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8')).name || ''
  } catch {
    /* ignore */
  }

  const byKey = new Map()
  for (const mode of ['production', 'development']) {
    let result
    try {
      result = runLicenseChecker(dir, mode)
    } catch (e) {
      console.warn(`[gen-licenses] ${scope} ${mode} 扫描失败：${e.message}`)
      continue
    }
    for (const [key, meta] of Object.entries(result)) {
      const at = key.lastIndexOf('@')
      const name = at > 0 ? key.slice(0, at) : key
      const version = at > 0 ? key.slice(at + 1) : ''
      if (name === selfName) continue
      // production 先入为准（同时属于 prod 的包归运行时）。
      if (byKey.has(key) && mode === 'development') continue
      byKey.set(key, {
        name,
        version,
        license: normLicense(meta.licenses),
        author: typeof meta.publisher === 'string' ? meta.publisher : '',
        url: typeof meta.repository === 'string' ? meta.repository : '',
        scope,
        ecosystem: 'npm',
        type: mode === 'production' ? 'runtime' : 'dev',
        licenseText: readLicenseText(meta.licenseFile),
      })
    }
  }
  return [...byKey.values()]
}

// ─────────────────────────────────── Go ───────────────────────────────────

/** go list -m all → [{ path, version, dir }]（跳过主模块/无版本）。 */
function goModules() {
  const out = execSync(`go list -m -f "{{.Path}}\t{{.Version}}\t{{.Dir}}\t{{.Main}}" all`, {
    cwd: REPO_ROOT,
    maxBuffer: MAX_BUFFER,
    stdio: ['ignore', 'pipe', 'ignore'],
  }).toString()
  const mods = []
  for (const line of out.split(/\r?\n/)) {
    if (!line.trim()) continue
    const [path, version, dir, main] = line.split('\t')
    if (main === 'true' || !version) continue
    mods.push({ path, version, dir })
  }
  return mods
}

/** go-licenses 可执行路径（GOPATH/bin），不存在返回空。 */
function goLicensesBin() {
  try {
    const gobin = execSync('go env GOPATH', { stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim()
    const bin = join(gobin, 'bin', process.platform === 'win32' ? 'go-licenses.exe' : 'go-licenses')
    return existsSync(bin) ? bin : ''
  } catch {
    return ''
  }
}

/** 扫 Go 依赖：go-licenses 选「真正链接」的模块集 + go list 补版本/全文。 */
function scanGo() {
  let mods
  try {
    mods = goModules()
  } catch (e) {
    console.warn(`[gen-licenses] 跳过 Go：go list 失败：${e.message}`)
    return []
  }
  // 最长前缀匹配：包路径 → 模块。
  const sorted = [...mods].sort((a, b) => b.path.length - a.path.length)
  const moduleOf = (pkgPath) => sorted.find((m) => pkgPath === m.path || pkgPath.startsWith(m.path + '/'))

  const bin = goLicensesBin()
  const byPath = new Map()
  if (bin) {
    try {
      const out = execSync(`"${bin}" csv ./...`, {
        cwd: REPO_ROOT,
        maxBuffer: MAX_BUFFER,
        stdio: ['ignore', 'pipe', 'ignore'],
      }).toString()
      for (const line of out.split(/\r?\n/)) {
        if (!line.trim()) continue
        const [pkgPath, , licenseType] = line.split(',')
        const mod = moduleOf(pkgPath)
        if (!mod || byPath.has(mod.path)) continue
        byPath.set(mod.path, {
          name: mod.path,
          version: mod.version,
          license: licenseType || 'Unknown',
          author: mod.path.split('/').slice(0, 2).join('/'),
          url: `https://${mod.path}`,
          scope: 'go',
          ecosystem: 'go',
          type: 'runtime',
          licenseText: readLicenseFromDir(mod.dir),
        })
      }
    } catch (e) {
      console.warn(`[gen-licenses] go-licenses 扫描失败，回退 go list 启发式：${e.message}`)
    }
  } else {
    console.warn('[gen-licenses] 未找到 go-licenses（go install github.com/google/go-licenses@latest），回退 go list 启发式')
  }

  // 回退/兜底：go-licenses 没覆盖到的模块用 go list + 启发式补全（保证版本/全文不丢）。
  if (byPath.size === 0) {
    for (const mod of mods) {
      const text = readLicenseFromDir(mod.dir)
      byPath.set(mod.path, {
        name: mod.path,
        version: mod.version,
        license: detectLicense(text),
        author: mod.path.split('/').slice(0, 2).join('/'),
        url: `https://${mod.path}`,
        scope: 'go',
        ecosystem: 'go',
        type: 'runtime',
        licenseText: text,
      })
    }
  }
  return [...byPath.values()]
}

// ─────────────────────────────────── 主流程 ───────────────────────────────────

const deps = [...scanNpm('web', WEB_DIR), ...scanNpm('bot-worker', BOT_DIR), ...scanGo()]
deps.sort((a, b) => a.scope.localeCompare(b.scope) || a.name.localeCompare(b.name))

const manifest = {
  // generatedAt 由调用方/CI 注入更稳；脚本内用环境变量或留空（避免不可复现 diff 噪声）。
  generatedAt: process.env.LICENSES_GENERATED_AT || '',
  dependencies: deps,
}

mkdirSync(dirname(OUT_FILE), { recursive: true })
writeFileSync(OUT_FILE, JSON.stringify(manifest, null, 2) + '\n', 'utf8')

const runtime = deps.filter((d) => d.type === 'runtime').length
const dev = deps.filter((d) => d.type === 'dev').length
console.log(`[gen-licenses] 写出 ${deps.length} 条依赖（运行时 ${runtime} / 开发 ${dev}）→ ${OUT_FILE}`)
