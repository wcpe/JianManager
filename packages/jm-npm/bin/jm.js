#!/usr/bin/env node
"use strict"

// `jm` 的 npm 启动器。
//
// 分发模型：主包 `@jianmanager/jm` 不含二进制，占位靠 `optionalDependencies` 引入
// 当前平台的 `@jianmanager/jm-<platform>`，由它携带真正的 Go 二进制。
//
// 为什么用平台分包而不是 postinstall 下载：
//   - 安装时无需访问 GitHub Releases，企业内网 / 代理环境也能装
//   - npm 的 `os` / `cpu` 字段天然做到「只装匹配平台的那一个」，其余包不会被下载
//   - 卸载 / 升级由 npm 统一管理，不留悬空文件
//
// 本文件的唯一职责：定位平台包里的可执行文件，并把进程完整交给它
// （继承 stdio 与退出码，不做任何输出包装）。

const { spawn } = require("node:child_process")
const { existsSync } = require("node:fs")
const { createRequire } = require("node:module")
const path = require("node:path")

/** 当前平台标识，与平台包命名一一对应。 */
function platformId() {
  return `${process.platform}-${process.arch}`
}

/** 平台包名；与发布脚本的命名保持一致。 */
function platformPackageName(id) {
  return `@jianmanager/jm-${id}`
}

/**
 * 二进制文件名。
 * Windows 需要 .exe 后缀；其余平台无后缀。
 */
function binaryName() {
  return process.platform === "win32" ? "jm.exe" : "jm"
}

/**
 * 定位平台包里的二进制。
 *
 * 优先用 Node 的模块解析（`require.resolve`）：它天然认识 npm 的平铺布局、
 * pnpm 的 `.pnpm` + symlink 布局，以及其他包管理器的变体，无需为每种布局
 * 各写一条硬编码路径。定位到平台包的 package.json 后再回溯到 bin/。
 *
 * 解析失败时才退回路径探测，用于 `resolve` 也失效的极端情况（例如某些
 * 严格隔离配置把可选依赖放在不可解析的位置）。两条路都失败时返回 null，
 * 由调用方给出可操作的提示。
 */
function findBinary() {
  const id = platformId()
  const pkg = platformPackageName(id)
  const bin = binaryName()

  // 首选：模块解析。
  try {
    const require_ = createRequire(__filename)
    const pkgJSON = require_.resolve(`${pkg}/package.json`)
    const candidate = path.join(path.dirname(pkgJSON), "bin", bin)
    if (existsSync(candidate)) return candidate
  } catch {
    // 解析不到（未安装 / 包管理器隔离），走下面的探测。
  }

  // 兜底：按已知布局逐条探测。
  // __dirname 形如 <...>/node_modules/@jianmanager/jm/bin，故 pkgRoot 是本包的
  // 上一级（即 @jianmanager 目录），平台包与它同级。
  const scopeDir = path.join(__dirname, "..", "..")
  const candidates = [
    path.join(scopeDir, `jm-${id}`, "bin", bin), // npm 平铺：同级 scope 目录
    path.join(__dirname, "..", "node_modules", pkg, "bin", bin), // pnpm：包内 node_modules
    path.join(scopeDir, "..", pkg, "bin", bin), // 兜底：上一级 node_modules
  ]

  for (const candidate of candidates) {
    if (existsSync(candidate)) return candidate
  }
  return null
}

function main() {
  const bin = findBinary()
  if (bin === null) {
    const id = platformId()
    process.stderr.write(
      [
        `jm: 找不到当前平台（${id}）的二进制包。`,
        "",
        "可能原因：",
        `  · 安装时跳过了可选依赖（--no-optional / --omit=optional）`,
        `  · npm 镜像未同步 @jianmanager/jm-${id}`,
        "",
        "解决方式任选其一：",
        `  1) 重新安装并允许可选依赖：npm install -g @jianmanager/jm`,
        `  2) 直接安装平台包：npm install -g @jianmanager/jm-${id}`,
        `  3) 不使用 npm：从 GitHub Releases 下载 jm-${id} 放到 PATH`,
        "",
      ].join("\n"),
    )
    process.exit(1)
  }

  // 透传全部参数，并沿用父进程 stdio。Windows 下 shell:false 是默认值，
  // 这里显式不设 shell，避免参数被 cmd 二次解释（含空格/引号的 path 会被拆开）。
  const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" })

  child.on("error", (err) => {
    process.stderr.write(`jm: 启动二进制失败: ${err.message}\n`)
    process.exit(1)
  })
  // 信号原样转发，让 Ctrl-C / SIGTERM 的退出语义与直接运行二进制一致。
  child.on("exit", (code, signal) => {
    if (signal !== null) {
      process.kill(process.pid, signal)
      return
    }
    process.exit(code === null ? 1 : code)
  })
}

main()
