// gen-licenses 契约测试:detectLicense 混合文本/BSD 变体,以及
// SHIPPED_TARGETS 与 release.yml 发货矩阵的一致性(矩阵漂移必红)。
// 运行:node --test scripts/gen-licenses.test.mjs
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { detectLicense, SHIPPED_TARGETS } from './gen-licenses.mjs'

const APACHE_HEAD = 'Apache License\nVersion 2.0, January 2004\nhttp://www.apache.org/licenses/'
const MIT_TEXT = 'The MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy'

test('单一 Apache-2.0 文本', () => {
  assert.equal(detectLicense(APACHE_HEAD + '\n TERMS AND CONDITIONS'), 'Apache-2.0')
})

test('yaml.v3 式混合文本返回 Apache-2.0 AND MIT(首命中即返回会丢尾部 MIT)', () => {
  const text = APACHE_HEAD + '\n\nThe following files were ported from libyaml\n\n' + MIT_TEXT
  assert.equal(detectLicense(text), 'Apache-2.0 AND MIT')
})

test('经典 Neither the name 第三条款 → BSD-3-Clause', () => {
  const text =
    'Redistribution and use in source and binary forms, with or without modification, are permitted ' +
    'provided that the following conditions are met:\n\n' +
    '1. Redistributions of source code must retain the above copyright notice.\n' +
    '2. Redistributions in binary form must reproduce the above copyright notice.\n' +
    '3. Neither the name of the project nor the names of its contributors may be used to endorse.'
  assert.equal(detectLicense(text), 'BSD-3-Clause')
})

test('go-difflib 式 may-not-be-used-to-endorse 第三条款 → BSD-3-Clause', () => {
  const text =
    'Redistribution and use in source and binary forms, with or without modification, are permitted ' +
    'provided that the following conditions are met:\n\n' +
    '   * Redistributions of source code must retain the above copyright notice.\n' +
    '   * Redistributions in binary form must reproduce the above copyright notice.\n' +
    '   * The name of the author may not be used to endorse or promote products derived from this software.'
  assert.equal(detectLicense(text), 'BSD-3-Clause')
})

test('仅两条款 → BSD-2-Clause', () => {
  const text =
    'Redistribution and use in source and binary forms, with or without modification, are permitted ' +
    'provided that the following conditions are met:\n\n' +
    '1. Redistributions of source code must retain the above copyright notice.\n' +
    '2. Redistributions in binary form must reproduce the above copyright notice.'
  assert.equal(detectLicense(text), 'BSD-2-Clause')
})

test('纯 MIT 文本', () => {
  assert.equal(detectLicense(MIT_TEXT), 'MIT')
})

test('不可识别文本 → Unknown', () => {
  assert.equal(detectLicense('some readme without any license text'), 'Unknown')
  assert.equal(detectLicense(''), 'Unknown')
})

test('SHIPPED_TARGETS 覆盖 release.yml 实际发行矩阵', () => {
  // 直接解析 release.yml:改了发货矩阵而不动 SHIPPED_TARGETS,本测试必须红。
  const yml = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')

  // CP/Worker/jmctl 发货矩阵:build 段「交叉编译 4 个二进制」的 matrix os/arch 条目
  const buildStart = yml.indexOf('交叉编译 4 个二进制')
  const jmStart = yml.indexOf('交叉编译 jm 客户端')
  assert.ok(buildStart > 0 && jmStart > buildStart, 'release.yml 结构变更,需同步更新本测试的锚点')
  const buildSection = yml.slice(buildStart, jmStart)
  // { os: X, arch: Y } 两侧空格容忍,防止 YAML 空格变化误杀/漏检
  const expectedFull = [...buildSection.matchAll(/- \{\s*os:\s*(\w+),\s*arch:\s*(\w+)/g)]
    .map((m) => `${m[1]}/${m[2]}`)
    .sort()
  const actualFull = SHIPPED_TARGETS.filter((t) => t.pkgs === './...')
    .map((t) => `${t.goos}/${t.arch}`)
    .sort()
  assert.deepEqual(actualFull, expectedFull)

  // 各交叉编译步骤的 for target 列表 → 对应 ./apps 包路径;未被全量
  // 扫描覆盖的目标必须按该包单独扫描,漏配则清单会多算/漏算依赖。
  const covered = new Set(expectedFull)
  const scopedSteps = [
    { marker: '交叉编译 jm 客户端', pkgs: './apps/jm' },
    { marker: '交叉编译 jmctl', pkgs: './apps/jmctl' },
  ]
  const expectedScoped = []
  for (const { marker, pkgs } of scopedSteps) {
    const start = yml.indexOf(marker)
    assert.ok(start > buildStart, `release.yml 缺少「${marker}」步骤,需同步更新本测试`)
    // 只框到本步骤边界(下一个 - name:),不能按「交叉编译」字样截:
    // 该字样在 jmctl 之后不再出现时切片会延到 smoke/release job,把不发货
    // 的脚本循环误算进发行目标。
    const end = yml.indexOf('- name:', start + marker.length)
    const section = yml.slice(start, end > start ? end : undefined)
    const targets = [...section.matchAll(/for target in ([^;]+); do/g)]
      .flatMap((m) => m[1].trim().split(/\s+/))
    assert.ok(targets.length > 0, `未解析到「${marker}」的 for target 列表`)
    for (const t of new Set(targets.filter((x) => !covered.has(x)))) {
      const [goos, arch] = t.split('/')
      expectedScoped.push({ goos, arch, pkgs })
    }
  }
  const actualScoped = SHIPPED_TARGETS.filter((t) => t.pkgs !== './...')
  const key = (t) => `${t.goos}/${t.arch}/${t.pkgs}`
  assert.deepEqual(
    actualScoped.map(key).sort(),
    expectedScoped.map(key).sort(),
  )
})
