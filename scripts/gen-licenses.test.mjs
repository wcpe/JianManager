// detectLicense 回归测试:混合许可文本(yaml.v3 式 Apache+MIT)与 BSD 两种第三条款写法。
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
  const expectedFull = [...buildSection.matchAll(/- \{ os: (\w+), arch: (\w+)/g)]
    .map((m) => `${m[1]}/${m[2]}`)
    .sort()
  const actualFull = SHIPPED_TARGETS.filter((t) => t.pkgs === './...')
    .map((t) => `${t.goos}/${t.arch}`)
    .sort()
  assert.deepEqual(actualFull, expectedFull)

  // jm 客户端 for target 列表:未被全量扫描覆盖的目标,必须按 ./apps/jm 单独扫描
  const jmctlStart = yml.indexOf('交叉编译 jmctl')
  assert.ok(jmctlStart > jmStart, 'release.yml 结构变更,需同步更新本测试的锚点')
  const jmSection = yml.slice(jmStart, jmctlStart)
  const jmTargets = [...jmSection.matchAll(/for target in ([^;]+); do/g)]
    .flatMap((m) => m[1].trim().split(/\s+/))
  assert.ok(jmTargets.length > 0, '未解析到 jm 客户端的 for target 列表')
  const covered = new Set(expectedFull)
  const expectedJm = [...new Set(jmTargets.filter((t) => !covered.has(t)))].sort()
  const actualJm = SHIPPED_TARGETS.filter((t) => t.pkgs !== './...')
  assert.deepEqual(
    actualJm.map((t) => `${t.goos}/${t.arch}`).sort(),
    expectedJm,
  )
  assert.ok(actualJm.every((t) => t.pkgs === './apps/jm'))
})
