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

test('ci.yml 包含独立 Go 质量门禁与日志联邦回归命令', () => {
  const ciWorkflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const goQualityStart = ciWorkflow.indexOf('\n  go-quality:')
  const goQualityEnd = ciWorkflow.indexOf('\n  agent-gate:', goQualityStart)
  assert.ok(goQualityStart >= 0, 'ci.yml 必须包含 go-quality job')
  assert.ok(goQualityEnd > goQualityStart, 'go-quality job 必须位于 agent-gate 之前并有完整边界')
  const goQuality = ciWorkflow.slice(goQualityStart, goQualityEnd)

  assert.match(goQuality, /actions\/checkout@v7/)
  assert.match(goQuality, /submodules:\s*false/)
  assert.match(goQuality, /actions\/setup-go@v7/)
  assert.match(goQuality, /go-version:\s*['"]1\.26\.2['"]/)
  assert.match(goQuality, /cache:\s*true/)
  assert.match(goQuality, /go build \.\/\.\.\./)
  assert.match(goQuality, /go vet \.\/\.\.\./)
  assert.match(goQuality, /go test \.\/internal\/controlplane\/router -run ['"]LogFederation\|WriteFederationError\|FederationPermissionScopeDigest['"] -count=1/)
})

test('ci.yml 必须同时在 Windows 上跑 Go 单测（堵平台分叉）', () => {
  // 【为何立这道契约】CP 与 Worker 都发布 windows 产物，但 Windows 上从未跑过 go test，
  // 导致 filepath.IsAbs 这类**编译期按 GOOS 绑定**的判据可以在 Linux 上全绿、在 Windows
  // 上绕过信任边界。修那个 bug 时把 Windows job 补上了，此处断言它不被静默删掉。
  const ciWorkflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')

  // core 分片 job：必须是 windows-latest + 4 片矩阵 + 复用同一个分片脚本。
  const winStart = ciWorkflow.indexOf('\n  go-test-windows:')
  assert.ok(winStart >= 0, 'ci.yml 必须包含 go-test-windows job（否则 Windows 无 Go 门禁）')
  const winEnd = ciWorkflow.indexOf('\n  go-test-windows-router:', winStart)
  assert.ok(winEnd > winStart, 'go-test-windows 必须有完整边界')
  const win = ciWorkflow.slice(winStart, winEnd)

  assert.match(win, /runs-on:\s*windows-latest/, 'Windows 单测必须真跑在 windows-latest 上')
  assert.match(win, /shard:\s*\[0,\s*1,\s*2,\s*3\]/, 'Windows core 必须与 Linux 同为 4 片')
  assert.match(win, /fail-fast:\s*false/, '一片红不得阻止其余片跑完')
  assert.match(win, /actions\/checkout@v7/)
  assert.match(win, /submodules:\s*false/)
  assert.match(win, /actions\/setup-go@v7/)
  assert.match(win, /go-version:\s*['"]1\.26\.2['"]/)
  assert.match(win, /cache:\s*true/)
  // 分片口径必须复用同一个脚本：否则同一包在两平台落在不同片，无法对比红/绿。
  assert.match(win, /bash scripts\/go-test\.sh shard \$\{\{ matrix\.shard \}\} 4/)

  // router 单独一个 job（Windows 上单包 480–520s，时限不同）。
  const routerStart = winEnd
  const routerEnd = ciWorkflow.indexOf('\n  agent-gate:', routerStart)
  assert.ok(routerEnd > routerStart, 'go-test-windows-router 必须有完整边界')
  const winRouter = ciWorkflow.slice(routerStart, routerEnd)
  assert.match(winRouter, /runs-on:\s*windows-latest/)
  assert.match(winRouter, /bash scripts\/go-test\.sh router/)
  assert.match(winRouter, /ROUTER_TIMEOUT:\s*\d+m/, 'Windows router 必须显式放宽时限')

  // 两个 Windows job 都必须关 cgo：Windows 默认开 cgo，
  // 而发布产物（Makefile）是 CGO_ENABLED=0；门禁口径要与产物一致。
  assert.equal(
    (win.match(/CGO_ENABLED:\s*'0'/g) || []).length, 1,
    'Windows core job 必须设 CGO_ENABLED=0',
  )
  assert.equal(
    (winRouter.match(/CGO_ENABLED:\s*'0'/g) || []).length, 1,
    'Windows router job 必须设 CGO_ENABLED=0',
  )
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

test('普通分支无同 SHA tag 的裸版本：软跳过不阻塞（正式发布走 tag 流水线）', () => {
  // 发版顺序常为「先推 master 再推 tag」；master release 若硬失败只制造噪音。
  assert.deepEqual(
    resolveReleaseMetadata({
      ref: 'refs/heads/master',
      sha: 'abcdef0123456789',
      sourceVersion: '0.18.0',
      exactTags: [],
    }),
    {
      version: '0.18.0',
      releaseTag: 'latest',
      isRelease: false,
      publishRelease: false,
    },
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
