import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const repoRoot = path.resolve(__dirname, '../../../..')

function readWorkflow(name: string): string {
  return readFileSync(path.join(repoRoot, '.github/workflows', name), 'utf8')
}

function expectCommandsInOrder(source: string, commands: string[]): void {
  let position = 0
  for (const command of commands) {
    const nextPosition = source.indexOf(command, position)
    expect(nextPosition, `缺少或顺序错误：${command}`).toBeGreaterThanOrEqual(position)
    position = nextPosition + command.length
  }
}

function workflowStepNames(source: string): string[] {
  return [...source.matchAll(/^\s+- name:\s*(.+)$/gm)].map((match) => match[1])
}

describe('发布工作流契约', () => {
  const ci = readWorkflow('ci.yml')
  const release = readWorkflow('release.yml')
  const playwright = readFileSync(
    path.join(repoRoot, 'apps/control-plane-web/playwright.config.ts'),
    'utf8',
  )

  it('CI 与发布工作流统一使用 Node.js 22', () => {
    expect(ci).toContain("NODE_VERSION: '22'")
    expect(release).toContain("NODE_VERSION: '22'")
  })

  it('工作流使用 Node.js 24 运行时对应的官方 Action 主版本', () => {
    expect(ci).toContain('actions/checkout@v7')
    expect(ci).toContain('actions/cache@v6')
    expect(ci).toContain('actions/setup-node@v7')
    expect(ci).toContain('actions/upload-artifact@v7')
    expect(release).toContain('actions/checkout@v7')
    expect(release).toContain('actions/cache@v6')
    expect(release).toContain('actions/setup-go@v7')
    expect(release).toContain('actions/setup-java@v5')
    expect(release).toContain('actions/setup-node@v7')
    expect(release).toContain('actions/upload-artifact@v7')
    expect(release).toContain('actions/download-artifact@v8')
  })

  it('可见步骤名称不携带需求编号', () => {
    expect(workflowStepNames(ci).join('\n')).not.toMatch(/\bFR-\d+/)
    expect(workflowStepNames(release).join('\n')).not.toMatch(/\bFR-\d+/)
  })

  it('CI 将 E2E 分成四个隔离分片并保留统一门禁', () => {
    expect(ci).toContain('shard: [1, 2, 3, 4]')
    expect(ci).toContain('name: Web E2E / Shard ${{ matrix.shard }} of 4')
    expect(ci).toContain('pnpm e2e --shard=${{ matrix.shard }}/4')
    expect(ci).toContain('name: web-e2e-test-results-${{ matrix.shard }}')
    expect(ci).toContain('needs: [web-static, web-e2e]')
  })

  it('CI 对偶发 E2E 重试一次且仅持续失败时阻断', () => {
    expect(playwright).toContain('retries: process.env.CI ? 1 : 0')
    expect(playwright).toContain('failOnFlakyTests: false')
    expect(playwright).toContain("['github']")
  })

  it('CI Bot Worker 在安装后依次通过四项门禁', () => {
    const botQuality = ci.slice(ci.indexOf('  bot-quality:'))

    expectCommandsInOrder(botQuality, [
      'run: npm ci',
      'npm run audit:prod',
      'npm run typecheck',
      'npm run lint',
      'npm run build',
    ])
  })

  it('发布元数据由独立脚本统一生成并供后续 job 消费', () => {
    const metadata = release.slice(
      release.indexOf('  metadata:'),
      release.indexOf('\n  prepare-embeds:'),
    )

    expect(metadata).toContain('node scripts/release-metadata.mjs')
    expect(metadata).toContain('version: ${{ steps.meta.outputs.version }}')
    expect(metadata).toContain('release_tag: ${{ steps.meta.outputs.release_tag }}')
    expect(metadata).toContain('publish_release: ${{ steps.meta.outputs.publish_release }}')
    expect(release).toContain('needs: metadata')
    expect(release).toContain('${{ needs.metadata.outputs.version }}')
    expect(release).not.toContain('0.0.0-dev+')
  })

  it('正式发布复用同提交 CI 结果且只保留 Go 门禁', () => {
    const verifyCI = release.slice(
      release.indexOf('  verify-ci:'),
      release.indexOf('\n  prepare-embeds:'),
    )

    expect(verifyCI).toContain('gh run list')
    expect(verifyCI).toContain('--workflow ci.yml')
    expect(verifyCI).toContain('--commit "${GITHUB_SHA}"')
    expect(verifyCI).toContain('gh run view')
    expect(verifyCI).toContain('--json status,conclusion')
    expect(release).toContain('\n  go-test:')
    expect(release).toContain('go test ./...')
    expect(release).not.toContain('pnpm exec vitest run')
    expect(release).not.toContain('pnpm e2e')
    expect(release).toContain('needs: [metadata, verify-ci]')
    expect(release).toContain("if: needs.metadata.outputs.publish_release == 'true'")
  })

  it('发布准备阶段通过 Bot Worker 门禁并生成内嵌资产', () => {
    const prepareEmbeds = release.slice(
      release.indexOf('  prepare-embeds:'),
      release.indexOf('\n  go-test:'),
    )

    expectCommandsInOrder(prepareEmbeds, [
      'npm --prefix apps/bot-worker ci',
      'npm --prefix apps/bot-worker run audit:prod',
      'npm --prefix apps/bot-worker run typecheck',
      'npm --prefix apps/bot-worker run lint',
      'npm --prefix apps/bot-worker run build',
      'go run ./scripts/embed-botworker.go',
      '--version "${{ needs.metadata.outputs.version }}"',
    ])
    expect(prepareEmbeds).toContain('internal/controlplane/embed/botworker/')
  })

  it('发布前在原生 runner 校验四个二进制的版本输出', () => {
    const smoke = release.slice(release.indexOf('  smoke:'), release.indexOf('\n  release:'))

    expect(smoke).toContain('ubuntu-latest')
    expect(smoke).toContain('windows-latest')
    expect(smoke).toContain('control-plane-linux-amd64')
    expect(smoke).toContain('worker-linux-amd64')
    expect(smoke).toContain('control-plane-windows-amd64.exe')
    expect(smoke).toContain('worker-windows-amd64.exe')
    expect(smoke).toContain('--version')
    expect(smoke).toContain('needs.metadata.outputs.version')
    expect(release).toContain('needs: [metadata, build, smoke]')
  })
})
