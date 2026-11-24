// detectLicense 回归测试:混合许可文本(yaml.v3 式 Apache+MIT)与 BSD 两种第三条款写法。
// 运行:node --test scripts/gen-licenses.test.mjs
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { detectLicense } from './gen-licenses.mjs'

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
