// 构建期依赖与许可证扫描（FR-135）：覆盖 Web、Bot Worker、Go、客户端更新器与 ServerProbe，
// 产出 apps/control-plane-web/public/licenses.json 供前端 /licenses 页读取。**构建期生成、不手维护**。
//
// 用法：node scripts/gen-licenses.mjs            （由 Makefile `gen-licenses` 调用，build 前置）
//
// npm：license-checker-rseidelsohn（web devDependency）分别按 --production / --development 扫，
//      运行时/开发分区即据此；作者取 publisher、链接取 repository、全文读 licenseFile。
// Go ：go-licenses csv（实际链接进二进制的包集，比 go.mod 全图更贴近「真正分发」），
//      版本/目录/全文用 go list -m 补全；go-licenses 不可用时回退 go list + 许可证启发式识别。
// Java：调用仓库自带 Gradle wrapper 的 dependencies 标准输出，只收发行物实际 runtime/taboo 依赖；
//       任一发行来源为空时直接失败，避免生成部分清单。

import { execSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, readdirSync, writeFileSync, mkdirSync, statSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(SCRIPT_DIR, '..')
// FR-283：前端迁 apps/control-plane-web（pnpm workspace，锁文件在仓库根 pnpm-lock.yaml）。
const WEB_DIR = join(REPO_ROOT, 'apps', 'control-plane-web')
const BOT_DIR = join(REPO_ROOT, 'apps', 'bot-worker')
const CLIENT_UPDATER_DIR = join(REPO_ROOT, 'client-updater')
const SERVERPROBE_DIR = join(REPO_ROOT, 'third_party', 'ServerProbe')
const OUT_FILE = join(WEB_DIR, 'public', 'licenses.json')
const REQUIRED_SCOPES = ['web', 'bot-worker', 'go', 'client-updater', 'serverprobe']

const MAX_BUFFER = 128 * 1024 * 1024
const MAX_LICENSE_TEXT = 64 * 1024
const LICENSE_FILE_RE = /^(LICENSE|LICENCE|COPYING|NOTICE|UNLICENSE)(?:[._-][\w.-]+)?$/i
const SCRIPT_FILE = fileURLToPath(import.meta.url)
const FORCE = process.argv.includes('--force') || process.env.LICENSES_FORCE === '1'
const DEPENDENCY_INPUT_FILES = [
  join(WEB_DIR, 'package.json'),
  join(REPO_ROOT, 'pnpm-lock.yaml'),
  join(BOT_DIR, 'package.json'),
  join(BOT_DIR, 'package-lock.json'),
  join(REPO_ROOT, 'go.mod'),
  join(REPO_ROOT, 'go.sum'),
  join(CLIENT_UPDATER_DIR, 'settings.gradle.kts'),
  join(CLIENT_UPDATER_DIR, 'build.gradle.kts'),
  join(CLIENT_UPDATER_DIR, 'gradle.properties'),
  join(CLIENT_UPDATER_DIR, 'gradle', 'wrapper', 'gradle-wrapper.properties'),
  join(CLIENT_UPDATER_DIR, 'wedge', 'build.gradle.kts'),
  join(CLIENT_UPDATER_DIR, 'updater-core', 'build.gradle.kts'),
  join(SERVERPROBE_DIR, 'settings.gradle.kts'),
  join(SERVERPROBE_DIR, 'build.gradle.kts'),
  join(SERVERPROBE_DIR, 'gradle.properties'),
  join(SERVERPROBE_DIR, 'gradle', 'wrapper', 'gradle-wrapper.properties'),
  join(SERVERPROBE_DIR, 'api', 'build.gradle.kts'),
  join(SERVERPROBE_DIR, 'project', 'core', 'build.gradle.kts'),
  join(SERVERPROBE_DIR, 'platform', 'platform-bukkit', 'build.gradle.kts'),
  join(SERVERPROBE_DIR, 'platform', 'platform-bungee', 'build.gradle.kts'),
  join(SERVERPROBE_DIR, 'plugin', 'build.gradle.kts'),
]
const FINGERPRINT_FILES = [...DEPENDENCY_INPUT_FILES, SCRIPT_FILE]

/** 计算依赖输入指纹；锁文件未变时可复用已有 licenses.json。 */
function sourceFingerprint() {
  const hash = createHash('sha256')
  for (const file of FINGERPRINT_FILES) {
    hash.update(file)
    hash.update('\0')
    if (existsSync(file)) {
      hash.update(readFileSync(file))
    }
    hash.update('\0')
  }
  return hash.digest('hex')
}

/** 尝试读取已有清单的输入指纹。 */
function existingSourceHash() {
  try {
    if (!existsSync(OUT_FILE)) return ''
    const manifest = JSON.parse(readFileSync(OUT_FILE, 'utf8'))
    return typeof manifest.sourceHash === 'string' ? manifest.sourceHash : ''
  } catch {
    return ''
  }
}

/** 缓存清单也必须包含全部发行来源，防止沿用旧版三源结果。 */
function existingManifestHasRequiredScopes() {
  try {
    const manifest = JSON.parse(readFileSync(OUT_FILE, 'utf8'))
    return REQUIRED_SCOPES.every((scope) => manifest.dependencies?.some((dependency) => dependency.scope === scope))
  } catch {
    return false
  }
}

/** 兼容旧清单：没有 sourceHash 时，只要依赖输入没比输出新，就快速复用。 */
function existingOutputIsFreshByMtime() {
  try {
    if (!existsSync(OUT_FILE)) return false
    const outMtime = statSync(OUT_FILE).mtimeMs
    return DEPENDENCY_INPUT_FILES.every((file) => !existsSync(file) || statSync(file).mtimeMs <= outMtime)
  } catch {
    return false
  }
}

/** 读许可证全文（截断到上限），失败返回空串。 */
function readLicenseText(file) {
  try {
    if (!file || !existsSync(file)) return ''
    return readFileSync(file, 'utf8').slice(0, MAX_LICENSE_TEXT)
  } catch {
    return ''
  }
}

/** 仅明确的许可证文件名可作为静态公开全文，避免误收 README 等任意依赖文本。 */
function readDeclaredLicenseText(file, license) {
  const identifier = normLicense(license).replace(/[^A-Za-z0-9.+() /-]/g, '').slice(0, 160) || 'Unknown'
  const fallback = `未找到可安全公开的许可证文件；依赖声明的许可证标识：${identifier}`
  if (!file || !LICENSE_FILE_RE.test(basename(file))) return fallback
  return readLicenseText(file) || fallback
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
        licenseText: readDeclaredLicenseText(meta.licenseFile, meta.licenses),
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

// ─────────────────────────────── Java / Gradle ───────────────────────────────

const MAVEN_METADATA_OVERRIDES = new Map([
  [
    'com.github.luben:zstd-jni',
    { license: 'BSD-2-Clause', author: 'Luben Karavelov', url: 'https://github.com/luben/zstd-jni' },
  ],
  ['org.ow2.asm:asm', { license: 'BSD-3-Clause', author: 'OW2', url: 'https://asm.ow2.io' }],
  ['org.ow2.asm:asm-commons', { license: 'BSD-3-Clause', author: 'OW2', url: 'https://asm.ow2.io' }],
  ['org.ow2.asm:asm-tree', { license: 'BSD-3-Clause', author: 'OW2', url: 'https://asm.ow2.io' }],
])

/** 从 Gradle dependencies 文本提取去重后的 Maven 坐标。 */
function parseGradleDependencies(output) {
  const coordinates = new Map()
  for (const line of output.split(/\r?\n/)) {
    const branch = line.match(/^[\s|+\\-]*---\s+(.+)$/)
    if (!branch || branch[1].startsWith('project ')) continue
    const matched = branch[1].match(/^([^:\s]+):([^:\s]+):([^\s]+)(?:\s+->\s+([^\s]+))?/)
    if (!matched) continue
    const [, group, artifact, requestedVersion, selectedVersion] = matched
    const version = selectedVersion || requestedVersion
    if (version === 'FAILED') throw new Error(`Gradle 依赖解析失败：${group}:${artifact}`)
    coordinates.set(`${group}:${artifact}`, { group, artifact, version })
  }
  return [...coordinates.values()]
}

/** 调用项目自带 wrapper 查询指定发行 classpath；可用 LICENSES_GRADLE_OFFLINE=1 强制离线。 */
function gradleDependencies(projectDir, projectPath, configuration) {
  const wrapper = join(projectDir, process.platform === 'win32' ? 'gradlew.bat' : 'gradlew')
  if (!existsSync(wrapper)) throw new Error(`缺少 Gradle wrapper：${wrapper}`)
  const offline = process.env.LICENSES_GRADLE_OFFLINE === '1' ? '--offline ' : ''
  const command = `"${wrapper}" --console=plain --quiet ${offline}${projectPath}:dependencies --configuration ${configuration}`
  const output = execSync(command, {
    cwd: projectDir,
    maxBuffer: MAX_BUFFER,
    stdio: ['ignore', 'pipe', 'pipe'],
  }).toString()
  return parseGradleDependencies(output)
}

/** Java 元数据保持确定性；未知坐标仍入清单，但不猜测许可证与项目链接。 */
function mavenMetadata(coordinate) {
  const name = `${coordinate.group}:${coordinate.artifact}`
  return MAVEN_METADATA_OVERRIDES.get(name) || { license: 'Unknown', author: coordinate.group, url: '' }
}

/** 扫描一个 Java 发行物；空结果视为构建错误，禁止静默生成残缺清单。 */
function scanGradle(scope, projectDir, projectPath, configuration) {
  let coordinates
  try {
    coordinates = gradleDependencies(projectDir, projectPath, configuration)
  } catch (error) {
    throw new Error(`${scope} 依赖扫描失败：${error.message}`)
  }
  if (coordinates.length === 0) throw new Error(`${scope} 依赖扫描结果为空`)
  return coordinates.map((coordinate) => ({
    name: `${coordinate.group}:${coordinate.artifact}`,
    version: coordinate.version,
    ...mavenMetadata(coordinate),
    scope,
    ecosystem: 'maven',
    type: 'runtime',
    licenseText: '',
  }))
}

/** 所有发行来源必须非空，避免任何扫描失败被包装成看似成功的部分结果。 */
function assertRequiredScopes(dependencies) {
  const missing = REQUIRED_SCOPES.filter(
    (scope) => !dependencies.some((dependency) => dependency.scope === scope),
  )
  if (missing.length > 0) throw new Error(`许可证清单缺少发行来源：${missing.join(', ')}`)
}

// ─────────────────────────────────── 主流程 ───────────────────────────────────

const sourceHash = sourceFingerprint()
const cachedHash = existingSourceHash()
if (
  !FORCE &&
  existingManifestHasRequiredScopes() &&
  (cachedHash === sourceHash || (!cachedHash && existingOutputIsFreshByMtime()))
) {
  console.log(`[gen-licenses] 依赖输入未变化，复用已有清单 → ${OUT_FILE}`)
  process.exit(0)
}

const deps = [
  ...scanNpm('web', WEB_DIR),
  ...scanNpm('bot-worker', BOT_DIR),
  ...scanGo(),
  ...scanGradle('client-updater', CLIENT_UPDATER_DIR, ':updater-core', 'runtimeClasspath'),
  ...scanGradle('serverprobe', SERVERPROBE_DIR, ':plugin', 'taboo'),
]
assertRequiredScopes(deps)
deps.sort((a, b) => a.scope.localeCompare(b.scope) || a.name.localeCompare(b.name))

const manifest = {
  // generatedAt 由调用方/CI 注入更稳；脚本内用环境变量或留空（避免不可复现 diff 噪声）。
  generatedAt: process.env.LICENSES_GENERATED_AT || '',
  sourceHash,
  dependencies: deps,
}

mkdirSync(dirname(OUT_FILE), { recursive: true })
writeFileSync(OUT_FILE, JSON.stringify(manifest, null, 2) + '\n', 'utf8')

const runtime = deps.filter((d) => d.type === 'runtime').length
const dev = deps.filter((d) => d.type === 'dev').length
console.log(`[gen-licenses] 写出 ${deps.length} 条依赖（运行时 ${runtime} / 开发 ${dev}）→ ${OUT_FILE}`)
