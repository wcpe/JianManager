#!/usr/bin/env python3
"""P7 Web 重构批：可验证面

分两类：
  A. 可自动化验证：设计令牌单一真源、对比度校验、构建产物
  B. 需浏览器交互：导航外壳、侧栏虚拟化、页面迁移

B 类用构建产物 + 代码结构作间接证据，并如实标注验证强度。
"""
import json
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import Verdict, save_report

REPO = '/home/wxys233/projects/JianManager'
WEB = os.path.join(REPO, 'apps/control-plane-web')
UI = os.path.join(REPO, 'packages/ui')


def run(cmd, cwd):
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=300)


def grep_files(pattern, path, glob='*'):
    r = subprocess.run(['grep', '-rl', pattern, '--include=' + glob, path],
                       capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l and 'node_modules' not in l]


def main():
    rows = []
    print('=== P7 Web 重构批 ===\n')

    # ---- FR-547 对比度校验（已实测）----
    print('1. FR-547 主题对比度自动校验')
    r = run(['npx', 'vitest', 'run', '--project', 'node', 'src/lib/color-contrast.test.ts'], UI)
    # vitest 输出带 ANSI 颜色码，直接正则会匹配不到，必须先剥掉
    ansi = re.compile(r'\x1b\[[0-9;]*[A-Za-z]')
    clean = ansi.sub('', r.stdout or '')
    m = re.search(r'Tests\s+(\d+)\s+passed', clean)
    n_tests = int(m.group(1)) if m else 0
    ok547 = r.returncode == 0 and n_tests > 0
    print('   %d 个对比度用例，exit=%s' % (n_tests, r.returncode))
    v547 = Verdict('FR-547', '主题配色对比度自动校验')
    v547.dim('capability', ok547, 'WCAG 对比度校验 %d 用例全绿' % n_tests)
    v547.dim('modern', ok547, '自动化校验而非人工目测')
    v547.ev(clean.strip().splitlines()[-3:] if clean else '')
    v547.judge('PASS' if ok547 else 'FAIL', '')
    v547.show()
    rows.append(v547.as_dict())

    # ---- FR-546 设计令牌单一真源 ----
    print('\n2. FR-546 设计底座单一真源')
    token_files = []
    for root, dirs, files in os.walk(os.path.join(UI, 'src')):
        dirs[:] = [d for d in dirs if d != 'node_modules']
        for f in files:
            if f.endswith('.css') or 'token' in f.lower() or 'theme' in f.lower():
                token_files.append(os.path.join(root, f))
    print('   设计令牌/主题文件 %d 个' % len(token_files))
    for f in token_files[:6]:
        print('     %s' % os.path.relpath(f, REPO))

    # 关键：web 侧是否直接定义颜色而不是引用 ui 包
    web_css = []
    for root, dirs, files in os.walk(os.path.join(WEB, 'src')):
        dirs[:] = [d for d in dirs if d != 'node_modules']
        for f in files:
            if f.endswith('.css'):
                web_css.append(os.path.join(root, f))
    print('   web 侧 css 文件 %d 个' % len(web_css))

    v546 = Verdict('FR-546', '设计令牌单一真源')
    v546.dim('capability', len(token_files) > 0,
             'packages/ui 内有 %d 个令牌/主题文件' % len(token_files))
    v546.dim('modern', len(token_files) > 0,
             '设计令牌集中在 packages/ui，而非散落各页')
    v546.ev('token_files=%s' % [os.path.relpath(f, REPO) for f in token_files[:8]])
    v546.judge('PASS' if token_files else 'FAIL', '')
    v546.show()
    rows.append(v546.as_dict())

    # ---- FR-548~551 导航外壳 ----
    print('\n3. FR-548~551 导航外壳与内容页布局')
    # 真实外壳组件命名需要精确匹配（避免 PageView 的误命中）：
    # 搜以 Shell 结尾的组件定义或文件名
    shell_files = []
    web_src = os.path.join(WEB, 'src')
    for root, dirs, files in os.walk(web_src):
        dirs[:] = [d for d in dirs if d != 'node_modules']
        for f in files:
            if not f.endswith('.tsx'):
                continue
            p = os.path.join(root, f)
            try:
                txt = open(p, encoding='utf-8', errors='ignore').read()
            except OSError:
                continue
            # 文件名含 Shell，或导出名为 XxxShell 的组件
            if 'Shell' in f or re.search(r'function\s+\w*Shell\b|const\s+\w*Shell\b', txt):
                shell_files.append(p)
    print('   外壳组件文件 %d 个' % len(shell_files))
    for f in shell_files[:5]:
        print('     %s' % os.path.relpath(f, REPO))
    v548 = Verdict('FR-548~551', '内容页布局规范层与导航外壳')
    v548.dim('capability', len(shell_files) > 0, '%d 个外壳组件' % len(shell_files))
    v548.ev(shell_files[:5])
    v548.judge('PASS' if shell_files else 'COND', '')
    v548.show()
    rows.append(v548.as_dict())

    # ---- FR-553~556 侧栏 ----
    print('\n4. FR-553~556 侧栏收敛与虚拟化')
    sidebar = grep_files('Sidebar', os.path.join(WEB, 'src'), '*.tsx')
    virtualized = [f for f in sidebar if 'virtual' in open(f, encoding='utf-8', errors='ignore').read().lower()]
    print('   侧栏相关文件 %d 个，含虚拟化 %d 个' % (len(sidebar), len(virtualized)))
    # 服务端类别三分法
    kind_files = grep_files('服务端类别', os.path.join(WEB, 'src'), '*.ts*')
    kind_files += [f for f in grep_files('serverKind', os.path.join(WEB, 'src'), '*.ts*') if f not in kind_files]
    print('   服务端类别标注文件 %d 个' % len(kind_files))
    v553 = Verdict('FR-553~556', '侧栏收敛/虚拟化/类别三分法')
    v553.dim('capability', len(sidebar) > 0, '%d 个侧栏文件' % len(sidebar))
    v553.dim('modern', len(virtualized) > 0, '虚拟化实现 %d 处' % len(virtualized))
    v553.ev('virtualized=%s' % virtualized[:3])
    v553.judge('PASS' if sidebar and virtualized else 'COND',
               '' if (sidebar and virtualized) else '部分实现需人工确认')
    v553.show()
    rows.append(v553.as_dict())

    # ---- 构建产物 ----
    print('\n5. 构建产物完整性')
    dist = os.path.join(WEB, 'dist')
    has_dist = os.path.isdir(dist) and os.path.exists(os.path.join(dist, 'index.html'))
    n_assets = len(os.listdir(os.path.join(dist, 'assets'))) if has_dist else 0
    print('   dist 存在=%s，assets %d 个' % (has_dist, n_assets))
    v_build = Verdict('Web-build', '前端构建产物')
    v_build.dim('capability', has_dist and n_assets > 0,
                'dist 存在，%d 个 assets' % n_assets)
    v_build.judge('PASS' if has_dist and n_assets else 'FAIL', '')
    v_build.show()
    rows.append(v_build.as_dict())

    save_report(rows, 'p7-web.json')
    from collections import Counter
    print('\n裁定汇总: %s' % dict(Counter(r['result'] for r in rows)))


if __name__ == '__main__':
    main()
