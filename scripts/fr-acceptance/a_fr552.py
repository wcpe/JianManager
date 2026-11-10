#!/usr/bin/env python3
"""A 批：代码内 FR/ADR 编号全量清理（FR-552）验收。

这条的验收就是跑门禁本身：stock 模式要 0 命中，diff 模式不能报新引入。
关键是要**反向自检**——门禁真的有牙齿，不是恒过。
"""
import json
import os
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import Verdict, save_report

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SCRIPT = '.claude/scripts/check-no-ref-in-code.sh'


def run_gate(*args):
    cmd = ['sh', SCRIPT] + list(args)
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True, timeout=300)
    return r.returncode, (r.stdout + r.stderr)


def main():
    rows = []
    print('=== A 批：代码内编号清理 ===\n')

    v = Verdict('FR-552', '代码内 FR/ADR 编号全量清理')

    # 1) stock 模式：存量期望 0
    print('1. 存量模式（全仓代码，期望 0 命中）')
    rc_stock, out_stock = run_gate('--stock')
    print('   exit=%d  %s' % (rc_stock, out_stock.strip().splitlines()[-1] if out_stock.strip() else ''))
    stock_clean = (rc_stock == 0)

    # 2) diff 模式：不能报新引入
    print('\n2. diff 模式（新引入，期望 0）')
    rc_diff, out_diff = run_gate()
    print('   exit=%d  %s' % (rc_diff, out_diff.strip().splitlines()[-1] if out_diff.strip() else ''))
    diff_clean = (rc_diff == 0)

    # 3) 生产代码单独确认（豁免只给验收工具目录）
    print('\n3. 生产代码单独确认')
    r = subprocess.run(
        ['git', 'grep', '-InE', r'(^|[^A-Za-z0-9_-])(FR|ADR)-[0-9]+',
         '--', 'apps', 'internal', 'packages', 'proto', 'client-updater', 'configs',
         ':!*.md', ':!*.json', ':!*.pb.go'],
        cwd=REPO, capture_output=True, text=True)
    prod_hits = [l for l in r.stdout.splitlines() if l and 'no-ref-check' not in l]
    print('   生产代码命中: %d' % len(prod_hits))

    # 4) 文件名也不得带编号
    r2 = subprocess.run(
        ['git', 'ls-files', '--', 'apps', 'internal', 'packages', 'scripts',
         'proto', 'client-updater', 'configs'],
        cwd=REPO, capture_output=True, text=True)
    import re
    name_hits = [f for f in r2.stdout.splitlines()
                 if re.search(r'(^|[^a-z0-9])(fr|adr)[0-9]+', f, re.I)
                 and not f.startswith('scripts/fr-acceptance/')]
    print('   文件名命中: %d' % len(name_hits))

    # 5) 反向自检：门禁真的能拦住吗？
    # 在一个生产文件里临时注入一行编号，看 stock 是否转红；然后还原。
    print('\n4. 反向自检（门禁是否真有牙齿）')
    probe = os.path.join(REPO, 'internal', 'platform', 'httpclient', 'proxy_mock_test.go')
    injected = False
    try:
        orig = open(probe, encoding='utf-8').read()
        with open(probe, 'a', encoding='utf-8') as f:
            f.write('\n// FR-9999 门禁反向自检临时行\n')
        injected = True
        rc_bad, out_bad = run_gate('--stock')
        caught = (rc_bad != 0) and ('FR-9999' in out_bad)
        print('   注入编号后 stock exit=%d，门禁捕获=%s' % (rc_bad, caught))
    finally:
        if injected:
            open(probe, 'w', encoding='utf-8').write(orig)
            print('   已还原被注入的文件')

    # 确认还原后仍然干净
    rc_after, _ = run_gate('--stock')
    restored = (rc_after == 0)
    print('   还原后 stock exit=%d' % rc_after)

    v.dim('capability', stock_clean, '存量模式 0 命中')
    v.dim('capability', diff_clean, 'diff 模式无新引入')
    v.dim('capability', len(prod_hits) == 0, '生产代码命中 %d' % len(prod_hits))
    v.dim('capability', len(name_hits) == 0, '文件名命中 %d' % len(name_hits))
    v.dim('semantics', caught and restored,
          '反向自检：注入编号能拦住，移除后恢复通过（门禁非恒过）')

    v.ev('stock exit=%d' % rc_stock)
    v.ev('diff exit=%d' % rc_diff)
    v.ev('prod hits=%d name hits=%d' % (len(prod_hits), len(name_hits)))
    v.ev('reverse check: injected caught=%s restored=%s' % (caught, restored))

    if stock_clean and diff_clean and len(prod_hits) == 0 and len(name_hits) == 0 and caught:
        v.judge('PASS',
                '存量归零（生产代码 0 处，文件名 0 个）；门禁反向自检通过——'
                '人为注入 FR-9999 能立即拦住，移除后恢复通过，不是恒过闸')
    elif stock_clean and diff_clean:
        v.judge('COND', '存量已归零，但反向自检未通过（门禁可能失效）')
    else:
        v.judge('FAIL', '仍有存量编号或 diff 报新引入')
    v.show()
    rows.append(v.as_dict())

    p = save_report(rows, 'a-fr552.json')
    print('\n已保存: %s' % p)
    return 0


if __name__ == '__main__':
    sys.exit(main())
