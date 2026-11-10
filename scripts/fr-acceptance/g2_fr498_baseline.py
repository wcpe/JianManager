#!/usr/bin/env python3
"""G' 批：FR-498 四条保留基线的真机采集。

FR-498 延后的原因是「索引持久化 p95」失去测量对象（SQLite 索引已无）。
但它**明确保留了四条基线**：Worker CPU/内存、VL 内存、查询 p95。
本轮把这四条真正量出来，让这条从「无法判定」变成「有基线、只是不吹 60 源」。

口径边界（必须写进结论）：
  当前环境 8 实例，不是 60 源。四条读数是「8 实例口径」，
  不得外推为 60 源容量基线——这是 FR-498 原文自己强调的边界。
"""
import json
import os
import re
import statistics
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report



def ps_table():
    r = subprocess.run(['ps', '-eo', 'pid,rss,pcpu,args', '--no-headers'],
                       capture_output=True, text=True)
    return r.stdout.splitlines()


def sum_rss_mb(rows, matcher, exclude=()):
    total = 0
    pids = []
    for line in rows:
        parts = line.split(None, 3)
        if len(parts) < 4:
            continue
        pid, rss, _cpu, args = parts
        if matcher not in args:
            continue
        if any(x in args for x in exclude):
            continue
        total += int(rss)
        pids.append(pid)
    return total / 1024.0, pids


def main():
    rows_out = []
    print('=== G\' 批：FR-498 四条基线真机采集 ===\n')

    rows = ps_table()

    # ── 1. Worker CPU / 内存 ──
    print('1. Worker CPU / 内存')
    # 注意：`/srv/jm/bin/jianmanager-worker` 会同时匹配主进程与所有 daemon 子进程，
    # 直接求和会把 daemon 算两遍（上一版就得到 254.4MB 这种不准的数）。
    # 正确口径：主进程单独取（排除 daemon），daemon 子进程另列。
    worker_main_rss, worker_main_pids = sum_rss_mb(
        rows, '/srv/jm/bin/jianmanager-worker', exclude=('daemon',))
    worker_daemon_rss, daemon_pids = sum_rss_mb(rows, 'jianmanager-worker daemon')
    print('   主进程 RSS %.1f MB（pid=%s）' % (worker_main_rss, ','.join(worker_main_pids)))
    print('   daemon 子进程 %d 个，合计 RSS %.1f MB'
          % (len(daemon_pids), worker_daemon_rss))

    # CPU 采样：取主进程 %cpu 三次
    cpu_samples = []
    for _ in range(3):
        r = subprocess.run(['ps', '-o', 'pcpu=', '-p', worker_main_pids[0]],
                           capture_output=True, text=True)
        v = r.stdout.strip()
        if v:
            cpu_samples.append(float(v))
        time.sleep(0.5)
    cpu_p50 = statistics.median(cpu_samples) if cpu_samples else -1
    print('   CPU 采样 %s%% → p50=%.1f%%' % (cpu_samples, cpu_p50))

    # ── 2. VL 内存 ──
    print('\n2. VictoriaLogs 内存')
    vl_rss, vl_pids = sum_rss_mb(rows, 'victoria-logs')
    print('   VL 进程 %d 个，合计 RSS %.1f MB（pids=%s）'
          % (len(vl_pids), vl_rss, ','.join(vl_pids)))

    # ── 3. CP 内存（补一个对照）──
    print('\n3. CP 内存')
    # 这台机器上同时跑着两套：/srv/dev（开发用）与 /srv/jm（被验收的测试环境）。
    # 只算 /srv/jm，否则会把开发套的内存混进来（上一版就是 373MB，
    # 而实际 /srv/jm 的 CP 只有 263MB，/srv/dev 那 110MB 不属本口径）。
    cp_rss, cp_pids = sum_rss_mb(rows, '/srv/jm/bin/jianmanager-cp')
    dev_cp_rss, dev_cp_pids = sum_rss_mb(rows, '/srv/dev/bin/control-plane')
    print('   /srv/jm CP RSS %.1f MB（pids=%s）' % (cp_rss, ','.join(cp_pids)))
    if dev_cp_pids:
        print('   （另有 /srv/dev 开发套 CP %.1f MB，pid=%s，不计入本口径）'
              % (dev_cp_rss, ','.join(dev_cp_pids)))

    # ── 4. 查询 p95（必须真返回数据）──
    print('\n4. 查询延迟 p95（含数据校验）')
    # 用 RFC3339 时间；每次换日期以绕缓存。
    days = ['2026-11-10', '2026-11-09', '2026-11-08', '2026-11-07', '2026-11-06',
            '2026-11-05', '2026-11-04', '2026-11-03']
    lat = []
    records = 0
    for d in days:
        t0 = time.time()
        st, body = api('/api/v1/logs/federation/search?limit=20&from=%sT00:00:00Z' % d)
        ms = (time.time() - t0) * 1000
        if st == 200 and isinstance(body, dict):
            lat.append(ms)
            records += len(body.get('items') or [])
    if lat:
        lat.sort()
        p50 = lat[len(lat) // 2]
        p95 = lat[min(len(lat) - 1, int(len(lat) * 0.95))]
        print('   有效查询 %d 次，累计返回 %d 条' % (len(lat), records))
        print('   延迟 ms: %s' % ['%.1f' % x for x in lat])
        print('   p50=%.1fms  p95=%.1fms' % (p50, p95))
    else:
        p50 = p95 = -1
        print('   查询均未成功，读数无效')

    # ── 装配裁定 ──
    v = Verdict('FR-498', '四条保留基线的真机量化（8 实例口径）')
    v.dim('perf', p95 > 0 and records > 0,
          '查询 p95=%.1fms，%d 条实际数据' % (p95, records) if p95 > 0 else '查询无效')
    v.dim('capability', worker_main_rss > 0, 'Worker RSS %.1fMB / CPU p50 %.1f%%'
          % (worker_main_rss, cpu_p50))
    v.dim('capability', vl_rss > 0, 'VL RSS %.1fMB' % vl_rss)
    v.dim('semantics', True,
          '口径如实：当前 %d 实例，非 60 源，读数不得外推'
          % len((api('/api/v1/instances?pageSize=200')[1] or [])))

    v.ev('worker_main_rss_mb=%.1f worker_daemon_rss_mb=%.1f daemon_count=%d'
         % (worker_main_rss, worker_daemon_rss, len(daemon_pids)))
    v.ev('worker_cpu_samples=%s p50=%.1f' % (cpu_samples, cpu_p50))
    v.ev('vl_rss_mb=%.1f cp_rss_mb=%.1f' % (vl_rss, cp_rss))
    v.ev('query lat_ms=%s p50=%.1f p95=%.1f records=%d'
         % (['%.1f' % x for x in lat], p50, p95, records))

    if p95 > 0 and records > 0:
        v.judge('COND',
                '四条基线已有真机量化读数：Worker RSS %.1fMB / CPU p50 %.1f%% / '
                'VL RSS %.1fMB / 查询 p50 %.1fms·p95 %.1fms（%d 条实际数据）。'
                '维持「已延后」是因为原主验收线「索引持久化 p95」失去对象'
                '（SQLite 索引已无，Catalog 为纯内存 map）；保留的四条现在有基线了，'
                '但**口径是 8 实例，不是 60 源**，不得外推。'
                % (worker_main_rss, cpu_p50, vl_rss, p50, p95, records))
    else:
        v.judge('FAIL', '真机读数采集失败')
    v.show()
    rows_out.append(v.as_dict())

    p = save_report(rows_out, 'g2-fr498-baseline.json')
    print('\n已保存: %s' % p)
    return 0


if __name__ == '__main__':
    sys.exit(main())
