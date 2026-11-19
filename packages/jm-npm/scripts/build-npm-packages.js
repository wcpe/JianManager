#!/usr/bin/env node
"use strict"

// `jm` 的 npm 平台分包发布脚本（FR-567）。
//
// 设计：一次 `go build` 产出各平台二进制，随后为每个平台生成一个独立 npm 包目录
// （`@jianmanager/jm-<platform>`），最后按需发布。主包 `@jianmanager/jm` 通过
// optionalDependencies 引入它们，npm 依据 `os` / `cpu` 字段只装匹配当前平台的那个。
//
// 用法：
//   node scripts/build-npm-packages.mjs            # 只生成包目录，不发布
//   node scripts/build-npm-packages.mjs --publish  # 生成并发布全部
//   node scripts/build-npm-packages.mjs --publish --dry-run   # 只看 npm 会发布什么
//
// 前置：Go 工具链可用（自动交叉编译）。

const { execFileSync, spawnSync } = require("node:child_process")
const { existsSync, mkdirSync, readFileSync, writeFileSync, copyFileSync, rmSync } = require("node:fs")
const path = require("node:path")

// 本脚本位于 packages/jm-npm/scripts/，上溯三级才是仓库根。
const repoRoot = path.resolve(__dirname, "..", "..", "..")
const pkgRoot = path.join(repoRoot, "packages", "jm-npm")
const buildDir = path.join(repoRoot, ".tmp", "jm-npm-build")

// 平台清单：npm 的 process.platform-process.arch → Go 的 GOOS/GOARCH。
//
// 与 packages/jm-npm/package.json 的 optionalDependencies 必须逐条对应：
// 少一个平台，那类机器安装后就没有二进制可用（主包会明确报错并给出补救方式）。
const platforms = [
  { npm: "linux-x64", goos: "linux", goarch: "amd64", ext: "" },
  { npm: "linux-arm64", goos: "linux", goarch: "arm64", ext: "" },
  { npm: "darwin-x64", goos: "darwin", goarch: "amd64", ext: "" },
  { npm: "darwin-arm64", goos: "darwin", goarch: "arm64", ext: "" },
  { npm: "win32-x64", goos: "windows", goarch: "amd64", ext: ".exe" },
]

const argv = new Set(process.argv.slice(2))
const doPublish = argv.has("--publish")
const dryRun = argv.has("--dry-run")

const mainPkg = JSON.parse(readFileSync(path.join(pkgRoot, "package.json"), "utf8"))
const version = mainPkg.version

function run(cmd, args, opts = {}) {
  console.log(`  $ ${cmd} ${args.join(" ")}`)
  execFileSync(cmd, args, { stdio: "inherit", cwd: repoRoot, ...opts })
}

/**
 * 校验平台清单与 optionalDependencies 完全一致。
 *
 * 这是本脚本最重要的一条自检：两处清单各自演进时极易漏改一侧，
 * 而后果是「某平台用户装上后没有二进制」——只有那个平台的用户会遇到，
 * 在 CI 与作者机器上完全看不出来。
 */
function assertManifestConsistency() {
  const declared = Object.keys(mainPkg.optionalDependencies || {})
  const generated = platforms.map((p) => `@jianmanager/jm-${p.npm}`)
  const missing = generated.filter((n) => !declared.includes(n))
  const extra = declared.filter((n) => !generated.includes(n))
  if (missing.length > 0 || extra.length > 0) {
    throw new Error(
      `平台清单与 optionalDependencies 不一致：\n` +
        (missing.length ? `  optionalDependencies 缺少: ${missing.join(", ")}\n` : "") +
        (extra.length ? `  optionalDependencies 多出: ${extra.join(", ")}\n` : ""),
    )
  }
}

/** 生成单个平台包目录并放入二进制。 */
function buildPlatformPackage(p) {
  const name = `@jianmanager/jm-${p.npm}`
  const dir = path.join(buildDir, `jm-${p.npm}`)
  const binDir = path.join(dir, "bin")
  rmSync(dir, { recursive: true, force: true })
  mkdirSync(binDir, { recursive: true })

  const binary = path.join(repoRoot, "dist", `jm-${p.goos}-${p.goarch}${p.ext}`)
  if (!existsSync(binary)) {
    throw new Error(`缺少二进制 ${binary}；先运行 make dist-jm`)
  }
  copyFileSync(binary, path.join(binDir, `jm${p.ext}`))

  // os / cpu 是 npm 的安装期过滤条件：不匹配的包不会被下载。
  const osName = p.npm.split("-")[0]
  const cpuName = p.npm.split("-").slice(1).join("-")

  writeFileSync(
    path.join(dir, "package.json"),
    JSON.stringify(
      {
        name,
        version,
        private: false,
        description: `jm 的 ${p.npm} 平台二进制（请安装 @jianmanager/jm 而非本包）。`,
        os: [osName],
        cpu: [cpuName],
        files: ["bin"],
        publishConfig: { access: "public" },
        engines: { node: ">=18" },
      },
      null,
      2,
    ) + "\n",
  )
  writeFileSync(
    path.join(dir, "README.md"),
    `# ${name}\n\n` +
      `本包是 [@jianmanager/jm](https://www.npmjs.com/package/@jianmanager/jm) 的 ${p.npm} 平台二进制，` +
      `由主包按平台自动选用。请安装主包：\n\n\`\`\`bash\nnpm install -g @jianmanager/jm\n\`\`\`\n`,
  )

  console.log(`  已生成 ${name} (${version})`)
  return dir
}

/** 发布一个包目录。 */
function publish(dir) {
  const args = ["publish", "--access", "public"]
  if (dryRun) args.push("--dry-run")
  const r = spawnSync("npm", args, { cwd: dir, stdio: "inherit", shell: process.platform === "win32" })
  if (r.status !== 0) throw new Error(`发布失败: ${dir}`)
}

function main() {
  console.log(`准备 jm npm 包（版本 ${version}）`)
  assertManifestConsistency()

  if (!existsSync(path.join(repoRoot, "dist"))) {
    throw new Error("dist/ 不存在；先运行 make dist-jm")
  }

  mkdirSync(buildDir, { recursive: true })
  const dirs = platforms.map(buildPlatformPackage)

  if (!doPublish) {
    console.log(`\n已生成到 ${buildDir}；加 --publish 才会发布。`)
    return
  }

  console.log("\n发布平台分包…")
  for (const dir of dirs) publish(dir)

  console.log("\n发布主包…")
  publish(pkgRoot)

  console.log(dryRun ? "\n完成（dry-run，未真正发布）" : "\n全部发布完成")
}

try {
  main()
} catch (err) {
  console.error(`\n错误: ${err.message}`)
  process.exit(1)
}
