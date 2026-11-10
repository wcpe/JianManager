#!/usr/bin/env python3
"""P2 日志平台批：已废弃 FR 的「实现对象不存在性」验收

口径：这 6 条 FR 已标 ❌ 已废弃。验收不是验功能，而是确认
「其实现对象确实已不存在」——若对象仍在且仍可运行，则废弃声明不诚实。
"""
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import Verdict, save_report

REPO = '/home/wxys233/projects/JianManager'


def grep_count(pattern, extra_exclude=None):
    """统计生产代码（排除测试）里匹配的出现次数。"""
    cmd = ['grep', '-rn', pattern, '--include=*.go', os.path.join(REPO, 'internal')]
    p = subprocess.run(cmd, capture_output=True, text=True)
    hits = []
    for line in p.stdout.splitlines():
        if '_test.go' in line:
            continue
        if extra_exclude and extra_exclude in line:
            continue
        hits.append(line)
    return hits


def path_exists(rel):
    return os.path.exists(os.path.join(REPO, rel))


def check(fr, title, checks, expect_absent=True):
    """checks: [(描述, 是否命中)]，命中=True 表示对象仍存在。"""
    v = Verdict(fr, title)
    still_there = []
    gone = []
    for desc, hit in checks:
        if hit:
            still_there.append(desc)
        else:
            gone.append(desc)

    if expect_absent:
        ok = len(still_there) == 0
        v.dim('capability', True, '已废弃 FR，口径为确认对象已移除')
        v.dim('modern', ok,
              ('仍有残留: ' + ', '.join(still_there)) if not ok else '对象已全部移除')
        v.ev('不存在: ' + (', '.join(gone) if gone else '无'))
        if still_there:
            v.ev('仍存在: ' + ', '.join(still_there))
            v.judge('COND', '废弃声明与代码不完全一致，需逐项确认是否为其他 FR 的复用件')
        else:
            v.judge('PASS', '实现对象均已移除，废弃声明诚实')
    v.show()
    return v


def main():
    rows = []
    print('=== P2 已废弃 FR 的对象不存在性验收 ===\n')

    rows.append(check('FR-474', '文件/STDIO/归档共用采集账本', [
        ('ArchiveImporter 类型', bool(grep_count('ArchiveImporter'))),
    ]).as_dict())

    # FR-478 特殊：PRD 声称「冷层与 Rehydrate 已删」，但实际代码完整存在且有生产装配调用
    # （apps/worker/main.go 的 cfg.LogArchive.Enabled 分支）。这不是“对象不存在”，
    # 而是“功能在代码里完整存活、只是真机配置未启用”。按最强语义必须如实判定。
    v478 = Verdict('FR-478', 'Deep Archive 与 Rehydrate')
    asm = grep_count('NewQueryBackend')
    reh = grep_count('NewRehydrateManager')
    v478.ev('生产装配调用点: %d 处' % len(asm))
    for line in asm[:3]:
        v478.ev('  ' + line.split(REPO + '/')[-1])
    v478.ev('NewRehydrateManager 调用（含测试）: %d 处' % len(reh))
    v478.dim('capability', True, 'archive 包完整：Provider/Registry/Rehydrate/QueryBackend/预算/Publisher')
    v478.dim('modern', True, 'S3/MinIO/Local 三种 Provider, 租约/预算/幂等均实现')
    v478.dim('semantics', False,
             'PRD 声明「冷层与 Rehydrate 已删」与代码不符：生产 main.go 仍完整装配该子系统')
    v478.judge('FAIL',
               '废弃声明不诚实：功能在代码中完整存活（生产装配路径存在，仅真机 cfg.LogArchive.Enabled 未开）。' 
               '要么恢复为已交付（补真机验收），要么真正删除代码。')
    v478.show()
    rows.append(v478.as_dict())

    rows.append(check('FR-481', '日志入库切换与 Legacy 只读', [
        ('LegacyLogReader 类型', bool(grep_count('LegacyLogReader'))),
    ]).as_dict())

    rows.append(check('FR-484', 'canonical 事件体落盘 compact 存储', [
        ('logs/eventstore 包', path_exists('internal/worker/logs/eventstore')),
        ('appendedEvents 写入链', bool(grep_count('appendedEvents'))),
    ]).as_dict())

    rows.append(check('FR-496', '采集索引迁移至本地 SQLite', [
        ('CollectLedger / collectLedger', bool(grep_count('ollectLedger'))),
        ('本地 index.db 引用', bool(grep_count('index\\.db'))),
    ]).as_dict())

    rows.append(check('FR-510', 'VL 不可用时的磁盘缓冲（spool）', [
        ('logs/spool 包', path_exists('internal/worker/logs/spool')),
    ]).as_dict())

    p = save_report(rows, 'p2-deprecated.json')
    print('\n已保存: %s' % p)

    from collections import Counter
    c = Counter(r['result'] for r in rows)
    print('\n裁定汇总: %s' % dict(c))


if __name__ == '__main__':
    main()
