// 发布元数据单一解析入口：源码版本、Git ref 与当前提交 tag 必须相互一致。
// 工作流只消费本脚本输出，避免 Bot 清单、二进制和 Release 名称各自推导版本。

import { execFileSync } from 'node:child_process'
import { appendFileSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(SCRIPT_DIR, '..')
const BARE_VERSION_RE = /^\d+\.\d+\.\d+$/
const DEVELOPMENT_VERSION_RE = /^\d+\.\d+\.\d+-(?:dev|rc\.\d+)$/

export function extractSourceVersion(source) {
  const match = String(source).match(/^var Version = "([^"]+)"$/m)
  if (!match) throw new Error('无法从 internal/version/version.go 读取 Version 真值')
  return match[1]
}

/**
 * 正式 tag 发版门禁：tag 指向的提交必须已在 origin/master（或 master）历史上。
 * 禁止从 feature/release/* 等非主干 tip 直接打 tag 出正式版（v0.19.0 误发教训）。
 * @param {string} sha 完整或短 SHA
 * @param {{ runGit?: typeof execFileSync, masterRefs?: string[] }} [opts]
 */
export function assertFormalTagCommitOnMaster(sha, opts = {}) {
  const runGit = opts.runGit || ((args) => execFileSync('git', args, {
    cwd: REPO_ROOT,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  }))
  const masterRefs = opts.masterRefs || ['origin/master', 'master']
  if (!sha || !String(sha).trim()) {
    throw new Error('正式 tag 校验缺少提交 SHA')
  }
  let masterRef = ''
  for (const ref of masterRefs) {
    try {
      runGit(['rev-parse', '--verify', ref])
      masterRef = ref
      break
    } catch {
      // try next
    }
  }
  if (!masterRef) {
    // CI checkout 可能尚未有 origin/master：尝试 fetch
    try {
      runGit(['fetch', '--no-tags', 'origin', 'master:refs/remotes/origin/master'])
      runGit(['rev-parse', '--verify', 'origin/master'])
      masterRef = 'origin/master'
    } catch {
      throw new Error('无法解析 origin/master/master，拒绝正式 tag 发版（需能校验 tag 是否在 master 上）')
    }
  }
  try {
    // merge-base --is-ancestor A B：A 是 B 的祖先则 0
    runGit(['merge-base', '--is-ancestor', sha, masterRef])
  } catch {
    throw new Error(
      `正式 tag 对应提交 ${String(sha).slice(0, 12)} 不在 ${masterRef} 历史上；` +
        '禁止从非 master 分支打 tag 发布正式版。请先将发版提交合入 master 再打 tag。',
    )
  }
}

export function resolveReleaseMetadata({ ref, sha, sourceVersion, exactTags = [], onMaster = true }) {
  if (!ref || !sha) throw new Error('缺少 Git ref 或提交 SHA')
  const shortSha = sha.slice(0, 7)

  if (ref.startsWith('refs/tags/')) {
    const releaseTag = ref.slice('refs/tags/'.length)
    if (!/^v\d+\.\d+\.\d+$/.test(releaseTag)) {
      throw new Error(`正式发布 tag 格式非法：${releaseTag}`)
    }
    const version = releaseTag.slice(1)
    if (sourceVersion !== version) {
      throw new Error(`源码版本 ${sourceVersion} 与正式 tag ${releaseTag} 不一致`)
    }
    // 单测可传 onMaster:true 跳过；main() 默认先 assertFormalTagCommitOnMaster 再调用
    if (onMaster === false) {
      throw new Error(
        `正式 tag 对应提交 ${String(sha).slice(0, 12)} 不在 master 历史上；` +
          '禁止从非 master 分支打 tag 发布正式版。请先将发版提交合入 master 再打 tag。',
      )
    }
    return { version, releaseTag, isRelease: true, publishRelease: true }
  }

  if (BARE_VERSION_RE.test(sourceVersion)) {
    if (!exactTags.includes(`v${sourceVersion}`)) {
      throw new Error(`普通分支源码使用裸版本 ${sourceVersion}，但当前提交没有同 SHA tag v${sourceVersion}`)
    }
    return {
      version: sourceVersion,
      releaseTag: 'latest',
      isRelease: false,
      publishRelease: false,
    }
  }

  if (!DEVELOPMENT_VERSION_RE.test(sourceVersion)) {
    throw new Error(`开发分支源码版本格式非法：${sourceVersion}`)
  }
  return {
    version: `${sourceVersion}+g${shortSha}`,
    releaseTag: 'latest',
    isRelease: false,
    publishRelease: true,
  }
}

function currentExactTags() {
  const output = execFileSync('git', ['tag', '--points-at', 'HEAD'], {
    cwd: REPO_ROOT,
    encoding: 'utf8',
  })
  return output.split(/\r?\n/).map((tag) => tag.trim()).filter(Boolean)
}

function writeOutputs(metadata) {
  const lines = [
    `version=${metadata.version}`,
    `release_tag=${metadata.releaseTag}`,
    `is_release=${metadata.isRelease}`,
    `publish_release=${metadata.publishRelease}`,
  ]
  if (process.env.GITHUB_OUTPUT) {
    appendFileSync(process.env.GITHUB_OUTPUT, `${lines.join('\n')}\n`)
  }
  process.stdout.write(`${JSON.stringify(metadata)}\n`)
}

function main() {
  try {
    const ref = process.env.GITHUB_REF
    const sha = process.env.GITHUB_SHA
    // 正式 v* tag：必须先证明提交已在 master 上，再解析元数据
    if (ref && ref.startsWith('refs/tags/v') && /^refs\/tags\/v\d+\.\d+\.\d+$/.test(ref)) {
      assertFormalTagCommitOnMaster(sha)
    }
    const versionSource = readFileSync(resolve(REPO_ROOT, 'internal/version/version.go'), 'utf8')
    const metadata = resolveReleaseMetadata({
      ref,
      sha,
      sourceVersion: extractSourceVersion(versionSource),
      exactTags: currentExactTags(),
      onMaster: true,
    })
    writeOutputs(metadata)
  } catch (error) {
    process.stderr.write(`${error.message}\n`)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main()
}
