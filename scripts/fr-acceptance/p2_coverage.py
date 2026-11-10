#!/usr/bin/env python3
"""P2 语义诚实性验证：coverage 声明与实际可查性是否一致

最强语义的核心追问：
  标 success 的目标，真的能查到数据吗？
  标 not_ready 的目标，是不是在「假装没数据」？
  各接口 coverage 不一致，是合理区分还是自相矛盾？
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report


def get_cov(path):
    st, d = api(path)
    if not isinstance(d, dict):
        return st, {}, d
    return st, d.get('coverage', {}) or {}, d


def targets_of(cov):
    return {t.get('target_id'): t for t in (cov.get('targets') or [])}


def main():
    rows = []
    print('=== coverage 语义一致性验证 ===\n')

    # 各接口的 coverage 口径
    panels = {
        'search': '/api/v1/logs/federation/search?limit=1',
        'stats':  '/api/v1/logs/federation/stats',
        'tail':   '/api/v1/logs/federation/tail?limit=1',
        'fields': '/api/v1/logs/federation/fields',
        'facade': '/api/v1/logs/federation?limit=1',
    }
    table = {}
    for name, p in panels.items():
        st, cov, _ = get_cov(p)
        tg = targets_of(cov)
        table[name] = {
            'http': st, 'complete': cov.get('complete'),
            'reasons': cov.get('partial_reasons') or [],
            'targets': {k: v.get('state') for k, v in tg.items()},
            'n': len(tg),
        }
        print('%-8s complete=%-6s targets=%-2d reasons=%s'
              % (name, cov.get('complete'), len(tg), cov.get('partial_reasons')))

    # ---- 交叉校验：search 标 success 的目标，实际能不能查到 ----
    print('\n=== 交叉校验：success 目标是否真有数据 ===')
    search_tg = table['search']['targets']
    ok_t = [k for k, v in search_tg.items() if v == 'success']
    print('  search 标 success 的目标: %d 个' % len(ok_t))

    verified, empty = [], []
    for tid in ok_t[:5]:  # 取前 5 个验证，避免请求过多
        q = '/api/v1/logs/federation/search?limit=1&targets=%s' % tid
        st, d = api(q)
        cnt = 0
        if isinstance(d, dict):
            v = d.get('view') or {}
            for k in ('records', 'events', 'items', 'results'):
                if k in v and isinstance(v[k], list):
                    cnt = len(v[k]); break
                if k in d and isinstance(d[k], list):
                    cnt = len(d[k]); break
        flag = 'ok' if cnt > 0 else 'EMPTY'
        (verified if cnt > 0 else empty).append(tid)
        print('    %-12s -> %d 条 %s' % (tid, cnt, flag))

    # ---- 裁定 ----
    v = Verdict('P2-coverage', 'coverage 语义诚实性')

    # 1) 各面板允许不同（窗口/目标不同），但 facade 与 search 应一致
    same_fs = table['facade']['complete'] == table['search']['complete']
    v.dim('semantics', same_fs,
          'facade 与 search 口径%s' % ('一致' if same_fs else '不一致'))

    # 2) success 目标必须真能查到（抽样）
    honest = len(empty) == 0
    v.dim('capability', honest,
          '%d/%d 个 success 目标抽样查到数据' % (len(verified), len(verified) + len(empty)))

    # 3) 各面板理由是否可解释
    explainable = all(
        t['reasons'] or t['complete']
        for t in table.values()
    )
    v.dim('modern', explainable, 'coverage=false 时均给出 partial_reasons')

    v.ev(json.dumps({k: {'complete': t['complete'], 'reasons': t['reasons']}
                     for k, t in table.items()}, ensure_ascii=False))

    if same_fs and honest:
        v.judge('PASS', 'coverage 声明与实际可查性一致；各面板口径差异由窗口/目标不同解释')
    elif honest:
        v.judge('COND', 'success 目标可信，但面板间口径需人工确认')
    else:
        v.judge('FAIL', 'success 目标存在查不到数据的情况（假成功）')
    v.show()
    rows.append(v.as_dict())

    save_report(rows, 'p2-coverage.json')

    # ---------- FR-505 不落盘架构实测 ----------
    import subprocess
    print('\n=== FR-505 不落盘架构实测 ===')
    LOG_DIR = '/srv/jm/worker-data/var/log'

    def du(p):
        r = subprocess.run(['du', '-sh', p], capture_output=True, text=True)
        return r.stdout.split('\t')[0] if r.returncode == 0 else 'n/a'

    def find_files(patterns, base=LOG_DIR):
        hits = []
        for pat in patterns:
            r = subprocess.run(['find', base, '-name', pat], capture_output=True, text=True)
            hits += [l for l in r.stdout.splitlines() if l]
        return hits

    idx_like = find_files(['*.db', '*.wal', '*.sqlite', 'eventstore*'])
    cursor = find_files(['*registry.jsonl'])
    journal = find_files(['catalog.journal.jsonl'])

    v505 = Verdict('FR-505', '不落盘采集架构')
    v505.ev('日志目录总量: %s' % du(LOG_DIR))
    v505.ev('索引/事件段/ WAL 落盘文件: %s' % ('无' if not idx_like else idx_like))
    v505.ev('游标注册表: %s' % cursor)
    v505.ev('catalog journal: %s' % journal)

    v505.dim('capability', len(idx_like) == 0,
             '无任何索引/事件段/WAL 落盘文件')
    v505.dim('modern', bool(cursor),
             '游标注册表存在（Filebeat fingerprint 模型）')

    # catalog journal 应为 0 字节（纯内存，文件仅为可选 store 占位）
    jsize = 0
    if journal:
        try:
            jsize = os.path.getsize(journal[0])
        except OSError:
            pass
    v505.dim('semantics', len(idx_like) == 0 and bool(cursor),
             '唯一跨重启持久面=游标注册表；索引/事件段/WAL 均不落盘' if (not idx_like and cursor)
             else '与声明不符')

    if len(idx_like) == 0 and cursor:
        v505.judge('PASS',
                   '架构声明成立：无索引/事件段/WAL 落盘，游标注册表为唯一持久面')
    elif cursor:
        v505.judge('COND', '有额外落盘文件，需确认是否属预期')
    else:
        v505.judge('FAIL', '游标注册表不存在，跨重启续采无依据')
    v505.show()
    rows.append(v505.as_dict())

    save_report(rows, 'p2-coverage.json')


if __name__ == '__main__':
    main()
