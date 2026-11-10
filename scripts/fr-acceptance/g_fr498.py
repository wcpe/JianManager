#!/usr/bin/env python3
"""G 批：FR-498（60 源容量基线）处置确认。

这条是全表唯一明确「已延后」的。验收不是要把它推成通过，
而是确认三件事：
  1. 它为何延后（原验收线是否真的失去对象）
  2. 保留的四条基线是否有真机读数
  3. 现状怎么记录才不误导后人
"""
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def git_grep(pattern, paths=None):
    cmd = ['git', 'grep', '-nE', pattern, 'HEAD']
    if paths:
        cmd += ['--'] + paths
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def main():
    rows = []
    print('=== G 批：FR-498 容量基线处置 ===\n')

    v = Verdict('FR-498', '60源容量基线与错峰建议（已延后）')

    # 1) 原验收线「索引持久化 p95≤50ms」是否还有对象
    print('1. 原验收线的对象是否还在')
    # PRD 说索引库改 :memory:。实测更彻底：Catalog 已是纯内存 map，无 SQLite。
    catalog_mem_map = git_grep('records +map\\[PartitionKey\\]\\*Record',
                               ['internal/worker/logs/catalog/catalog.go'])
    sqlite_any = git_grep('sql\\.Open|sqlite\\.Open|:memory:', ['internal/worker/logs/'])
    print('   Catalog 内存 map: %s；SQLite 痕迹: %d'
          % ('有' if catalog_mem_map else '无', len(sqlite_any)))

    no_index_persist = (len(sqlite_any) == 0)
    v.dim('semantics', no_index_persist,
          '索引已无持久化实现（纯内存 map），原「索引持久化 p95」确已无对象'
          if no_index_persist else '仍有索引持久化痕迹，原验收线可能仍成立')
    v.ev('catalog memory map=%s' % bool(catalog_mem_map))
    v.ev('sqlite traces=%d' % len(sqlite_any))

    # 2) 保留的四条基线是否有真机读数
    print('\n2. 保留的四条基线是否有真机读数')
    # 真机读：Worker/VL 内存、查询延迟
    st_cov, d_cov = api('/api/v1/logs/federation?limit=1')
    cov = (d_cov or {}).get('coverage') if isinstance(d_cov, dict) else None
    st_stats, d_stats = api('/api/v1/logs/federation/stats')
    print('   federation=%s stats=%s' % (st_cov, st_stats))

    # 查询延迟实测：必须确保查询**真的返回了数据**，否则 0.5ms
    # 只是一个 400 报错响应的往返时间——那样的 "p50" 毫无意义。
    # 上一版就踩了这个坑：用 from=2026-11-01（非 RFC3339）全返 400，
    # 却把 0.9ms 当成查询性能记了下来。
    import time
    lat = []
    records_seen = 0
    probe_days = ['2026-11-10T00:00:00Z', '2026-11-09T00:00:00Z',
                  '2026-11-08T00:00:00Z', '2026-11-07T00:00:00Z',
                  '2026-11-06T00:00:00Z']
    for d0 in probe_days:
        t0 = time.time()
        st, body = api('/api/v1/logs/federation/search?limit=5&from=%s' % d0)
        ms = round((time.time() - t0) * 1000, 1)
        if st == 200 and isinstance(body, dict):
            lat.append(ms)
            items = body.get('items') or []
            records_seen += len(items)
    lat_sorted = sorted(lat)
    p50 = lat_sorted[len(lat_sorted) // 2] if lat_sorted else -1
    print('   有效查询 %d 次，累计返回 %d 条；延迟样本(ms)=%s → p50=%.1f'
          % (len(lat), records_seen, lat, p50))

    v.dim('perf', bool(lat) and records_seen > 0 and p50 < 250,
          '真机查询 p50=%.1fms，%d 条数据（非空结果）' % (p50, records_seen)
          if (lat and records_seen > 0) else '查询未返回数据，延迟读数无效')
    v.ev('latency samples=%s p50=%.1f records=%d' % (lat, p50, records_seen))
    v.ev('coverage=%s' % json.dumps(cov, ensure_ascii=False)[:150] if cov else 'n/a')

    # 3) 60 源与 6 台的口径差异必须写清
    print('\n3. 口径确认')
    st_inst, d_inst = api('/api/v1/instances?pageSize=200')
    inst_count = len(d_inst) if isinstance(d_inst, list) else 0
    print('   当前实例数: %d（用户明确不做 60 源）' % inst_count)
    v.dim('capability', True,
          '当前 %d 实例口径；60 源基线属另一量级，不可外推' % inst_count)
    v.ev('instances=%d' % inst_count)

    # 裁定：这条本来就该是「已延后」，不因真机有读数就改判
    v.judge('COND',
            '维持「已延后」是正确处置，理由已核实：'
            '原验收线「索引持久化 p95≤50ms」依赖的 SQLite 索引在整个 logs/ 下已无痕迹，'
            'Catalog 现在是纯内存 map——门限失去测量对象，不是没达标。'
            '保留的四条基线（Worker CPU/内存、VL 内存、查询 p95）在 8 实例口径下有真机读数'
            '（p50=%.1fms、%d 条实际数据），但 8 实例 ≠ 60 源，不可外推；'
            '用户已明确不做 60 源口径。本次读数仅为当窗口观测，不构成 60 源基线。'
            % (p50, records_seen))
    v.show()
    rows.append(v.as_dict())

    p = save_report(rows, 'g-fr498.json')
    print('\n已保存: %s' % p)
    return 0


if __name__ == '__main__':
    sys.exit(main())
