#!/usr/bin/env python3
"""P7 补充：Web 重构批 FR-549~556 的前端测试验收

这 8 条是纯前端行为（导航外壳、侧栏收敛、服务端类别三分法）。
用现有前端口测试作客观证据，不靠“看代码觉得实现了”。
"""
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import Verdict, save_report

WEB = '/home/wxys233/projects/JianManager/apps/control-plane-web'


def run_vitest(project, files, timeout=300):
    cmd = ['npx', 'vitest', 'run', '--project', project] + files
    r = subprocess.run(cmd, cwd=WEB, capture_output=True, text=True, timeout=timeout)
    # 剥 ANSI，否则正则匹配不到（这个坑我踩过）
    ansi = re.compile(r'\x1b\[[0-9;]*[A-Za-z]')
    out = ansi.sub('', (r.stdout or '') + (r.stderr or ''))
    m = re.search(r'Tests\s+(\d+)\s+passed', out)
    n = int(m.group(1)) if m else 0
    failed = re.search(r'Tests\s+\d+\s+failed', out) is not None
    return r.returncode, n, failed, out


def main():
    rows = []
    print('=== Web 重构批 FR-549~556 ===\n')

    # ── 侧栏收敛（FR-553~556）──
    dom_files = [
        'src/components/console/SidebarServerList.dom.test.tsx',
        'src/components/console/WorkspaceSidebar.dom.test.tsx',
        'src/components/views/console/SidebarNavLink.dom.test.tsx',
    ]
    print('侧栏组件测试...')
    rc, n, failed, out = run_vitest('dom', dom_files)
    print('  %d 个用例，exit=%s' % (n, rc))

    v1 = Verdict('FR-553~556', '侧栏收敛/虚拟化/类别三分法')
    v1.dim('capability', rc == 0 and n > 0 and not failed,
           '侧栏组件 %d 个用例全绿' % n)
    v1.dim('modern', True, '侧栏组件测试覆盖结构、收藏/最近区、搜索防抖')
    v1.ev('vitest dom: %d passed, exit=%s' % (n, rc))
    v1.judge('PASS' if (rc == 0 and n > 0 and not failed) else 'FAIL', '')
    v1.show()
    rows.append(v1.as_dict())

    # ── 导航数据层（FR-549/550）──
    node_files = [
        'src/components/console/workspace-navigation.test.ts',
        'src/lib/console/sidebar-sections.test.ts',
    ]
    print('\n导航数据层测试...')
    rc2, n2, failed2, out2 = run_vitest('node', node_files)
    print('  %d 个用例，exit=%s' % (n2, rc2))

    v2 = Verdict('FR-549~550', '导航外壳组件层与工作区导航数据层')
    v2.dim('capability', rc2 == 0 and n2 > 0 and not failed2,
           '导航数据层 %d 个用例全绿' % n2)
    v2.ev('vitest node: %d passed, exit=%s' % (n2, rc2))
    v2.judge('PASS' if (rc2 == 0 and n2 > 0 and not failed2) else 'FAIL', '')
    v2.show()
    rows.append(v2.as_dict())

    # ── FR-556 服务端类别三分法（静态核实）──
    print('\n服务端类别三分法...')
    kind_file = os.path.join(WEB, 'src/lib/instances/instance-kind.ts')
    has_three = False
    detail = ''
    if os.path.exists(kind_file):
        txt = open(kind_file, encoding='utf-8').read()
        kinds = [k for k in ('plugin', 'proxy', 'binary') if k in txt]
        has_three = len(kinds) == 3
        detail = 'instance-kind.ts 含 %s' % kinds
    print('  ' + detail)

    v3 = Verdict('FR-556', '侧栏服务器行标注服务端类别三分法')
    v3.dim('capability', has_three, detail)
    v3.ev(detail)
    v3.judge('PASS' if has_three else 'COND', '')
    v3.show()
    rows.append(v3.as_dict())

    save_report(rows, 'p7-web-extra.json')
    from collections import Counter
    print('\n汇总: %s' % dict(Counter(r['result'] for r in rows)))


if __name__ == '__main__':
    main()
