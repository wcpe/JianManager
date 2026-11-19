#!/usr/bin/env node
"use strict"

// `jm` 的 npm 启动器（FR-567）。
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
 * 定位平台包根目录。
 *
 * 不用 `require.resolve` 直接找二进制：可选依赖在某些包管理器的严格模式下
 * 不会提升到本包的 node_modules，导致 resolve 失败。逐个候选路径探测更稳，
 * 且在失败时能给出具体找过的位置，便于用户自查。
 */
function findBinary() {
  const id = platformId()
  const pkg = platformPackageName(id)
  const bin = binaryName()
  // 包根：本文件在 <pkg>/bin/jm.js，上溯一级即包根。
  const pkgRoot = path.join(__dirname, "..")

  const candidates = [
    // 常规：npm 平铺安装，平台包与本包同处 <node_modules>/@jianmanager/
    path.join(pkgRoot, "..", `jm-${id}`, "bin", bin),
    // pnpm：平台包经符号链接挂在 <pkg>/node_modules 下
    path.join(pkgRoot, "node_modules", pkg, "bin", bin),
    // 全局安装的次级回退：直接在同级 node_modules 里找
    path.join(pkgRoot, "..", "..", pkg, "bin", bin),
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
