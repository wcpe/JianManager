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

export function resolveReleaseMetadata({ ref, sha, sourceVersion, exactTags = [] }) {
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
    const versionSource = readFileSync(resolve(REPO_ROOT, 'internal/version/version.go'), 'utf8')
    const metadata = resolveReleaseMetadata({
      ref: process.env.GITHUB_REF,
      sha: process.env.GITHUB_SHA,
      sourceVersion: extractSourceVersion(versionSource),
      exactTags: currentExactTags(),
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
