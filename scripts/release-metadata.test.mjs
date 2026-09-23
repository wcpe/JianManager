import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { extractSourceVersion, resolveReleaseMetadata } from './release-metadata.mjs'

test('master 推送与正式 tag 必须触发 CI', () => {
  const ciWorkflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const releaseWorkflow = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  // 单主干 GitHub Flow：CI 须在主干 master 推送时触发（主干始终可发布），
  // 短分支的门禁由 pull_request 承担，故 branches 只列 master。
  assert.match(ciWorkflow, /^  push:\r?\n(?:    .*\r?\n)*?    branches:\s*\[master\]\s*$/m)
  assert.match(ciWorkflow, /^  push:\r?\n(?:    .*\r?\n)*?    tags:\s*\['v\*'\]\s*$/m)
  assert.match(ciWorkflow, /^  pull_request:\s*$/m)
  assert.doesNotMatch(ciWorkflow, /actions\/setup-go@v5/)
  assert.match(ciWorkflow, /actions\/setup-go@v7/)
  assert.match(releaseWorkflow, /name: Checkout（读取源码版本与当前提交 tag）/)
  assert.doesNotMatch(releaseWorkflow, /third_party\/ServerProbe|submodules:\s*true/)
})

test('release.yml 只在正式 tag 触发，避免主干 push 上解析裸版本失败', () => {
  const releaseWorkflow = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  // push 只保留 tags：断言 tags 紧邻 push 之下，若有人加回 branches 会立刻不匹配。
  assert.match(releaseWorkflow, /^  push:\r?\n    tags:\s*\['v\*'\]\s*$/m)
  // 不得监听分支 push：主干/开发分支上的源码是裸 X.Y.Z 或 X.Y.Z-dev，
  // metadata 解析不出合法版本会直接失败（实测主干 push 的 metadata job 0s 报错）。
  assert.doesNotMatch(releaseWorkflow, /^  push:\r?\n(?:    .*\r?\n)*?    branches:/m)
  // 不得监听 pull_request：PR 的 ref 为 refs/pull/N/merge，同样解析不出合法版本。
  assert.doesNotMatch(releaseWorkflow, /^  pull_request:/m)
})

test('从 Go 源码读取版本真值', () => {
  assert.equal(extractSourceVersion('package version\nvar Version = "0.18.0-dev"\n'), '0.18.0-dev')
  assert.throws(() => extractSourceVersion('package version\n'), /读取 Version/)
})

test('正式 tag 分离 release 名称与二进制裸版本', () => {
  assert.deepEqual(
    resolveReleaseMetadata({
      ref: 'refs/tags/v0.18.0',
      sha: 'abcdef0123456789',
      sourceVersion: '0.18.0',
      exactTags: ['v0.18.0'],
      onMaster: true,
    }),
    {
      version: '0.18.0',
      releaseTag: 'v0.18.0',
      isRelease: true,
      publishRelease: true,
    },
  )
})

test('正式 tag 与源码裸版本不一致时拒绝发布', () => {
  assert.throws(
    () => resolveReleaseMetadata({
      ref: 'refs/tags/v0.18.0',
      sha: 'abcdef0123456789',
      sourceVersion: '0.18.1',
      exactTags: ['v0.18.0'],
      onMaster: true,
    }),
    /源码版本.*tag/,
  )
})

test('正式 tag 不在 master 历史上时拒绝发布', () => {
  assert.throws(
    () => resolveReleaseMetadata({
      ref: 'refs/tags/v0.19.0',
      sha: 'deadbeefcafebabe',
      sourceVersion: '0.19.0',
      exactTags: ['v0.19.0'],
      onMaster: false,
    }),
    /不在 master 历史上/,
  )
})

test('assertFormalTagCommitOnMaster 在 merge-base 失败时拒绝', async () => {
  const { assertFormalTagCommitOnMaster } = await import('./release-metadata.mjs')
  assert.throws(
    () => assertFormalTagCommitOnMaster('deadbeef', {
      masterRefs: ['origin/master'],
      runGit: (args) => {
        if (args[0] === 'rev-parse') return 'ok\n'
        if (args[0] === 'merge-base') {
          const err = new Error('not ancestor')
          err.status = 1
          throw err
        }
        return ''
      },
    }),
    /不在 origin\/master 历史上|禁止从非 master/,
  )
})

test('开发分支沿用源码目标版本并追加构建元数据', () => {
  assert.deepEqual(
    resolveReleaseMetadata({
      ref: 'refs/heads/master',
      sha: 'abcdef0123456789',
      sourceVersion: '0.18.0-dev',
      exactTags: [],
    }),
    {
      version: '0.18.0-dev+gabcdef0',
      releaseTag: 'latest',
      isRelease: false,
      publishRelease: true,
    },
  )
})

test('普通分支出现无同 SHA tag 的裸版本时拒绝构建', () => {
  assert.throws(
    () => resolveReleaseMetadata({
      ref: 'refs/heads/master',
      sha: 'abcdef0123456789',
      sourceVersion: '0.18.0',
      exactTags: [],
    }),
    /裸版本.*tag/,
  )
})

test('普通分支位于正式 tag 提交时不重复发布 latest', () => {
  assert.deepEqual(
    resolveReleaseMetadata({
      ref: 'refs/heads/master',
      sha: 'abcdef0123456789',
      sourceVersion: '0.18.0',
      exactTags: ['v0.18.0'],
    }),
    {
      version: '0.18.0',
      releaseTag: 'latest',
      isRelease: false,
      publishRelease: false,
    },
  )
})
