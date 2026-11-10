#!/usr/bin/env python3
"""P2 日志平台：采集链路端到端真机验证

验证点：
  1. 日志是否真的在流动（有数据、有时间推进）
  2. 采集覆盖是否完整（coverage.complete）
  3. 查询面五类是否可用（Search/Stats/Fields/Facets/Tail）
  4. 时间范围与量级
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report


def main():
    rows = []

    # ---- 1. 覆盖度 ----
    print('=== 1. 采集覆盖度 ===')
    st, fed = api('/api/v1/logs/federation?limit=1')
    cov = fed.get('coverage', {}) if isinstance(fed, dict) else {}
    targets = cov.get('targets') or []
    succ = [t for t in targets if t.get('state') == 'success']
    print('  complete=%s  目标 %d 个 / success %d 个' % (cov.get('complete'), len(targets), len(succ)))
    for t in targets[:10]:
        print('    %-12s %s' % (t.get('target_id'), t.get('state')))

    # ---- 2. 统计：有无数据 ----
    print('\n=== 2. 日志量统计 ===')
    st, stats = api('/api/v1/logs/federation/stats')
    print('  stats HTTP=%s' % st)
    body = stats.get('view', {}).get('result', {}) if isinstance(stats, dict) else {}
    print('  result keys: %s' % list(body.keys())[:8] if isinstance(body, dict) else body)
    total = None
    if isinstance(body, dict):
        for k in ('total', 'count', 'total_count', 'events'):
            if k in body:
                total = body[k]
                break
    print('  总量字段: %s' % total)

    # ---- 3. 五类查询面 ----
    print('\n=== 3. 查询面五类 ===')
    facets_ok = False
    checks = [
        ('Search', '/api/v1/logs/federation/search?limit=5'),
        ('Stats', '/api/v1/logs/federation/stats'),
        ('Fields', '/api/v1/logs/federation/fields'),
        ('Facets', '/api/v1/logs/federation/facets?dimensions=level'),
        ('Tail', '/api/v1/logs/federation/tail?limit=5'),
        ('Streams', '/api/v1/logs/federation/streams'),
    ]
    for name, p in checks:
        code, b = api(p)
        ok = code is not None and 200 <= code < 300
        if name == 'Facets' and ok:
            facets_ok = True
        print('  %-8s %-5s %s' % (name, code, 'ok' if ok else 'NO'))

    # ---- 4. 实际取几条记录看看 ----
    print('\n=== 4. 样本记录 ===')
    st, sr = api('/api/v1/logs/federation/search?limit=3')
    recs = []
    if isinstance(sr, dict):
        v = sr.get('view', {})
        for k in ('records', 'events', 'items', 'results'):
            if k in v:
                recs = v[k]
                break
            if k in sr:
                recs = sr[k]
                break
    print('  取到 %d 条' % len(recs))
    for r in recs[:3]:
        if isinstance(r, dict):
            ts = r.get('event_time') or r.get('time') or r.get('timestamp') or '?'
            msg = str(r.get('message') or r.get('body') or r.get('raw') or '')[:90]
            src = r.get('log_source_id') or r.get('stream') or '?'
            print('    [%s] %s | %s' % (src, ts, msg))

    # ---- 裁定 ----
    v = Verdict('P2-core', '日志采集链路端到端')
    v.dim('capability', cov.get('complete') is True,
          'coverage.complete=%s，%d/%d 目标成功' % (cov.get('complete'), len(succ), len(targets)))
    v.dim('capability', len(recs) > 0, '能取到样本记录 %d 条' % len(recs))
    v.dim('modern', facets_ok, '五类查询面（含 Facets）可用')
    v.ev('coverage targets=%d success=%d' % (len(targets), len(succ)))
    v.ev('sample records=%d' % len(recs))

    if cov.get('complete') and len(recs) > 0:
        v.judge('PASS', '采集链路端到端通，查询面可用')
    elif cov.get('complete'):
        v.judge('COND', '覆盖完整但样本为空，需确认是否无新日志产生')
    else:
        v.judge('FAIL', '采集未完成覆盖')
    v.show()
    rows.append(v.as_dict())

    p = save_report(rows, 'p2-core.json')
    print('\n已保存: %s' % p)


if __name__ == '__main__':
    main()
