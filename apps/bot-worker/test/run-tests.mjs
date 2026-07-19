import { readdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import process from 'node:process'
import { run } from 'node:test'
import { spec } from 'node:test/reporters'

const testRoot = resolve('test')
const files = await collectTests(testRoot)

if (files.length === 0) {
  process.stderr.write('未发现 test/**/*.test.mjs 测试文件\n')
  process.exitCode = 1
} else {
  run({ files })
    .on('test:fail', () => {
      process.exitCode = 1
    })
    .compose(spec)
    .pipe(process.stdout)
}

async function collectTests(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  const files = []
  for (const entry of entries) {
    const path = resolve(directory, entry.name)
    if (entry.isDirectory()) {
      files.push(...await collectTests(path))
    } else if (entry.isFile() && entry.name.endsWith('.test.mjs')) {
      files.push(path)
    }
  }
  return files.sort((left, right) => left.localeCompare(right))
}
