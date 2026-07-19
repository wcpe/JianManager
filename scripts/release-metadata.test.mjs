import assert from 'node:assert/strict'
import test from 'node:test'

import { extractSourceVersion, resolveReleaseMetadata } from './release-metadata.mjs'

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
    }),
    /源码版本.*tag/,
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
