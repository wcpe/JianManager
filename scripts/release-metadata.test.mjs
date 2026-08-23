import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { extractSourceVersion, resolveReleaseMetadata } from './release-metadata.mjs'

test('master 推送与正式 tag 必须触发 CI', () => {
  const ciWorkflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const releaseWorkflow = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  assert.match(ciWorkflow, /^    branches:\s*\['\*\*'\]\s*$/m)
  assert.match(ciWorkflow, /^  push:\r?\n(?:    .*\r?\n)*?    tags:\s*\['v\*'\]\s*$/m)
  assert.match(releaseWorkflow, /name: Checkout（读取源码版本与当前提交 tag）[\s\S]*?submodules: true/)
})

test('ServerProbe IoC 制品直连发布仓库，其余依赖保留聚合仓库', () => {
  const settings = readFileSync(new URL('../third_party/ServerProbe/settings.gradle.kts', import.meta.url), 'utf8')
  assert.match(
    settings,
    /maven\("https:\/\/maven\.wcpe\.top\/repository\/maven-releases\/"\)\s*\{\s*content\s*\{\s*includeGroup\("top\.wcpe\.taboolib\.ioc"\)/,
  )
  assert.match(
    settings,
    /^\s*maven\("https:\/\/maven\.wcpe\.top\/repository\/maven-public\/"\)\s*$/m,
  )
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
