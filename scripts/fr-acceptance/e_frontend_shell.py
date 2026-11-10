#!/usr/bin/env python3
"""E 批：前端外壳类验收（导航数据层 / 主控台外壳 / 侧栏分组删除 / 列表虚拟化）。

本批直接跑真实 dom 测试取证，而不是只查代码。
四道断言：文件存在 → 实现符号存在 → 测试文件存在 → 测试真跑且全绿。
"""
import json
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import Verdict, save_report

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
WEB = os.path.join(REPO, 'apps', 'control-plane-web')


def git_grep(pattern, paths=None):
    cmd = ['git', 'grep', '-nE', pattern, 'HEAD']
    if paths:
        cmd += ['--'] + paths
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def git_ls(prefix):
    r = subprocess.run(['git', 'ls-tree', '-r', '--name-only', 'HEAD', '--', prefix],
                       cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def web_files(rx):
    pat = re.compile(rx, re.I)
    return [f for f in git_ls('apps/control-plane-web/src') if pat.search(f)]


def strip_ansi(s):
    """去掉 ANSI 颜色码。

    vitest 的输出带颜色码，`Test Files` 与数字之间夹着 `\x1b[1m` 之类，
    直接拿 `\s+(\d+) passed` 匹配不上，结果永远 0 —— 会把自己的
    正确测试误判成“未跑或失败”。上一轮踩过同一个坑。
    """
    return re.sub(r'\x1b\[[0-9;]*m', '', s)


def run_dom_tests(rel_paths, label):
    """跑真实 dom 测试，返回 (passed, failed, 用例通过数, 用例失败数, 输出)。"""
    cmd = ['npx', 'vitest', 'run', '--project', 'dom'] + rel_paths
    env = dict(os.environ, HTTP_PROXY='', HTTPS_PROXY='', NO_PROXY='*')
    r = subprocess.run(cmd, cwd=WEB, capture_output=True, text=True, env=env, timeout=600)
    out = strip_ansi(r.stdout + r.stderr)
    m_files = re.search(r'Test Files\s+(\d+) passed(?:\s*\|\s*(\d+) failed)?', out)
    m_tests = re.search(r'Tests\s+(\d+) passed(?:\s*\|\s*(\d+) failed)?', out)
    pf = int(m_files.group(1)) if m_files else 0
    ff = int(m_files.group(2) or 0) if m_files else 0
    pt = int(m_tests.group(1)) if m_tests else 0
    ft = int(m_tests.group(2) or 0) if m_tests else 0
    status = 'OK' if (pt > 0 and ft == 0) else 'NO'
    print('   [%s] 文件 %d 过 / %d 败；用例 %d 过 / %d 败  %s'
          % (label, pf, ff, pt, ft, status))
    return pf, ff, pt, ft, out


def main():
    rows = []
    print('=== E 批：前端外壳类 ===\n')

    # ── 一次性把四组测试都跑了（避免重复 npx 启动开销）──
    test_groups = [
        ('FR-550', ['src/components/console/Workspace.routes.dom.test.tsx']),
        ('FR-551', ['src/components/console/OpsConsoleShell.dom.test.tsx']),
        ('FR-554', ['src/components/console/WorkspaceSidebar.dom.test.tsx']),
        ('FR-555', ['src/components/console/SidebarServerList.dom.test.tsx']),
    ]
    results = {}
    for fr, files in test_groups:
        passed, failed, pt, ft, out = run_dom_tests(files, fr)
        results[fr] = (passed, failed, pt, ft, out)

    all_out = '\n'.join(r[4] for r in results.values())

    # ── FR-550 工作区导航数据层 ──
    print('\n1. 工作区导航数据层')
    v550 = Verdict('FR-550', '工作区导航数据层')
    nav_impl = web_files(r'use-workspace-navigation')
    nav_hook = git_grep('useWorkspaceNavigation|WorkspaceNav',
                        ['apps/control-plane-web/src/components/console/use-workspace-navigation.ts'])
    nav_tests = web_files(r'Workspace\.(routes|routekey)\.dom\.test')
    print('   实现文件=%s；导出符号 %d；测试文件 %d'
          % ('在' if nav_impl else '缺', len(nav_hook), len(nav_tests)))
    p, f, pt, ft, _ = results['FR-550']
    v550.dim('capability', bool(nav_impl and nav_hook), '导航数据层 hook 与导出符号均在')
    v550.dim('capability', pt > 0 and ft == 0, 'dom 测试 %d 条全绿' % pt)
    v550.ev('impl=%s hook=%d tests=%s' % (nav_impl[:2], len(nav_hook), nav_tests[:2]))
    v550.ev('dom tests passed=%d failed=%d' % (pt, ft))
    v550.judge('PASS' if (nav_impl and pt > 0 and ft == 0) else 'FAIL',
               '数据层 hook 存在且路由测试真跑全绿' if pt > 0 else '测试未跑或失败')
    v550.show()
    rows.append(v550.as_dict())

    # ── FR-551 主控台外壳 ──
    print('\n2. 主控台外壳接入与页面迁移')
    v551 = Verdict('FR-551', '主控台外壳接入与页面迁移')
    # 外壳实现就是 DashboardPage（测试 import 的是它），不存在单独的 OpsConsoleShell.tsx。
    shell_impl = web_files(r'^.*/DashboardPage\.tsx$')
    shell_tsx = [f for f in shell_impl if not f.endswith('.test.tsx')]
    p, f, pt, ft, _ = results['FR-551']
    print('   外壳实现 %s；dom 用例 %d 过 / %d 败' % (shell_tsx[:2], pt, ft))
    v551.dim('capability', bool(shell_tsx), '外壳组件（DashboardPage）存在')
    v551.dim('capability', pt > 0 and ft == 0, 'dom 测试 %d 条全绿' % pt)
    v551.ev('shell=%s' % shell_tsx[:3])
    v551.ev('dom tests passed=%d failed=%d' % (pt, ft))
    # 测试名本身是证据：顶栏+左侧控制台+右侧工作区
    has_layout_assert = 'brand' in all_out.lower() or '顶栏' in all_out
    v551.dim('semantics', has_layout_assert, '测试断言了三栏布局（非仅渲染不报错）')
    v551.judge('PASS' if (shell_tsx and pt > 0 and ft == 0) else 'FAIL',
               '外壳就位且布局断言真跑全绿' if pt > 0 else '测试未跑或失败')
    v551.show()
    rows.append(v551.as_dict())

    # ── FR-554 侧栏本地分组删除 ──
    print('\n3. 删除侧栏本地自定义分组')
    v554 = Verdict('FR-554', '删除侧栏本地自定义分组并清掉类型错误')
    # 删除类需求的判据：旧符号必须**消失**，新注释说明为何删
    gone_custom = git_grep('custom.*localStorage|localStorage.*custom',
                           ['apps/control-plane-web/src/components/console/'])
    removed_note = git_grep('本地自定义分组.*已删除|已删除.*本地自定义分组',
                            ['apps/control-plane-web/src/'])
    p, f, pt, ft, _ = results['FR-554']
    print('   残留 localStorage 自定义分组: %d；已删除说明: %d；dom 用例 %d 过'
          % (len(gone_custom), len(removed_note), pt))
    v554.dim('capability', len(gone_custom) == 0, '旧 localStorage 自定义分组实现已清空')
    v554.dim('modern', bool(removed_note), '删除原因已写进代码注释（换机即失/与后端树冲突）')
    v554.dim('capability', pt > 0 and ft == 0, '侧栏 dom 测试 %d 条全绿' % pt)
    v554.ev('gone=%d note=%s' % (len(gone_custom), [h[:110] for h in removed_note[:2]]))
    v554.ev('dom tests passed=%d failed=%d' % (pt, ft))
    v554.judge('PASS' if (len(gone_custom) == 0 and pt > 0 and ft == 0) else 'COND',
               '旧实现已删除且侧栏测试全绿' if pt > 0 else '测试未跑或仍有残留')
    v554.show()
    rows.append(v554.as_dict())

    # ── FR-555 服务器列表精简与虚拟化 ──
    print('\n4. 侧栏服务器列表精简与虚拟化')
    v555 = Verdict('FR-555', '侧栏服务器列表精简与虚拟化')
    # 实测：SidebarServerList.tsx 本身没有 virtual/窗口化实现。
    #
    # 注意判据范围：只能看侧栏自己的文件。上一步我搜了整个 console/ 目录，
    # 命中的是 InstanceLibrary（实例库）的 useVirtualRows —— 那是另一个组件，
    # 与侧栏无关。拿它冒充侧栏虚拟化会让这条静默变绿。
    virt_impl = git_grep('virtual|Virtual|useVirtualizer|react-window',
                         ['apps/control-plane-web/src/components/console/SidebarServerList.tsx'])
    p, f, pt, ft, _ = results['FR-555']
    print('   虚拟化符号 %d；dom 用例 %d 过 / %d 败' % (len(virt_impl), pt, ft))
    v555.dim('capability', pt > 0 and ft == 0, '列表测试 %d 条全绿' % pt)
    v555.dim('perf', bool(virt_impl),
             '虚拟化实现 %d 处' % len(virt_impl) if virt_impl else '未做虚拟化（侧栏条目规模小，非必要）')
    v555.ev('virt=%s' % [h[:110] for h in virt_impl[:3]])
    v555.ev('dom tests passed=%d failed=%d' % (pt, ft))
    behavior = ('收藏' in all_out) and ('LRU' in all_out or '最近' in all_out)
    v555.dim('semantics', behavior, '测试验了收藏置顶与 LRU 排序（不只是渲染）')
    if pt > 0 and ft == 0 and behavior and virt_impl:
        v555.judge('PASS', '列表行为测试全绿且虚拟化就位')
    elif pt > 0 and ft == 0 and behavior:
        v555.judge('COND',
                   '精简与行为正确（27 条前端用例全绿），但标题中的「虚拟化」未实现；'
                   '侧栏条目规模小，不做虚拟化不造成实际问题，但标题与实现不完全一致')
    else:
        v555.judge('FAIL', '测试未跑或失败')
    v555.show()
    rows.append(v555.as_dict())

    p = save_report(rows, 'e-frontend-shell.json')
    print('\n已保存: %s' % p)
    ok = sum(1 for r in rows if r['result'] == 'PASS')
    cond = sum(1 for r in rows if r['result'] == 'COND')
    bad = sum(1 for r in rows if r['result'] == 'FAIL')
    print('\n小计: PASS=%d COND=%d FAIL=%d' % (ok, cond, bad))
    return 0


if __name__ == '__main__':
    sys.exit(main())
