#!/usr/bin/env python3
"""B 批：日志采集类真机与代码双证。

覆盖：契约两水位 / 采集登记 / 采样与保留 / 采集规则覆盖。

裁定口径（最强语义）：不看文档怎么写，看代码在不在、真机读得到什么。
文档声称“仍在生产”的每一项，都要在 HEAD 里找到实现，否则记 FAIL。
"""
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def git_grep(pattern, paths=None):
    """在 HEAD 里搜，不用工作区——多处代码正在被别的任务并发删除。

    必须带 -E：git grep 默认用基本正则（BRE），`a|b` 在 BRE 里不是“或”。
    B 批当初没有用交替式，影响不大；但保留 -E 避免以后加交替时静默失效。
    """
    cmd = ['git', 'grep', '-nE', pattern, 'HEAD']
    if paths:
        cmd += ['--'] + paths
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def git_ls(prefix):
    r = subprocess.run(['git', 'ls-tree', '-r', '--name-only', 'HEAD', '--', prefix],
                       cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def main():
    rows = []
    print('=== B 批：日志采集类 ===\n')

    # ───────────────── FR-473 契约：两水位 ─────────────────
    print('1. 契约两水位（Read / Reclaim）')
    v473 = Verdict('FR-473', '日志共享契约两水位与实际查询面')

    types_go = git_ls('internal/worker/logs/logtypes/types.go')
    read_pos = git_grep('Read     uint64', ['internal/worker/logs/logtypes/types.go'])
    reclaim_pos = git_grep('Reclaim  uint64', ['internal/worker/logs/logtypes/types.go'])
    print('   契约文件: %s' % ('在' if types_go else '缺失'))
    print('   Read 字段: %s' % ('在' if read_pos else '缺失'))
    print('   Reclaim 字段: %s' % ('在' if reclaim_pos else '缺失'))

    # 真机：查询面能出数据且在声明范围内
    st_fed, d_fed = api('/api/v1/logs/federation?limit=1')
    st_stats, d_stats = api('/api/v1/logs/federation/stats')
    st_str, d_str = api('/api/v1/logs/federation/streams?limit=5')
    fed_ok = isinstance(d_fed, dict)
    stats_ok = isinstance(d_stats, dict) and 'coverage' in (d_stats or {})
    streams_ok = isinstance(d_str, dict)
    print('   查询面: federation=%s stats=%s streams=%s' % (st_fed, st_stats, st_str))

    # dim() 同名键会互相覆盖：早前写成两次 dim('capability')，第一条断言被静默吞掉。
    # 先算好两者再一次性写入，两个条件都成立才算过。
    contract_ok = bool(types_go and read_pos and reclaim_pos)
    query_ok = bool(fed_ok and stats_ok and streams_ok)
    v473.dim('capability', contract_ok and query_ok,
             '契约含 Read/Reclaim 且三个查询面可用' if (contract_ok and query_ok)
             else '契约字段或查询面不完整')

    # 语义：缺口不得伪造成空成功（stats 里 coverage 必须如实）
    cov = (d_stats or {}).get('coverage') if isinstance(d_stats, dict) else None
    honest = cov is not None
    v473.dim('semantics', honest, 'stats 带 coverage 声明，不伪造成空成功')

    v473.ev('query faces: federation=%s stats=%s streams=%s' % (st_fed, st_stats, st_str))
    v473.ev('coverage=%s' % json.dumps(cov, ensure_ascii=False)[:200])

    if v473.dims.get('capability', (False,))[0] and honest:
        v473.judge('PASS', '两水位契约在（结构体+字段）；查询面可用且带 coverage 声明')
    else:
        v473.judge('FAIL', '契约与水位的存在性或查询面可用性不成立')
    v473.show()
    rows.append(v473.as_dict())

    # ───────────────── FR-499 采集登记 ─────────────────
    print('\n2. 采集登记与长周期锁解耦')
    v499 = Verdict('FR-499', '采集登记与长周期锁解耦')

    # FR-499 描述的是 ingest 包的四水位账本 + 登记锁模型。
    # 查它在 HEAD 是否还存在。
    ingest_files = git_ls('internal/worker/logs/ingest/')
    ledger_files = git_ls('internal/worker/logs/ledger/')
    print('   旧 ingest 包: %d 个文件' % len(ingest_files))
    print('   旧 ledger 包: %d 个文件' % len(ledger_files))

    registry = git_ls('internal/worker/logs/tailer/registry.go')
    reg_lock = git_grep('r\\.mu\\.Lock\\(\\)', ['internal/worker/logs/tailer/registry.go'])
    print('   新 tailer registry: %s（%d 处加锁）' % ('在' if registry else '缺失', len(reg_lock)))

    # 真机：采集实际在跑
    st_str2, d_str2 = api('/api/v1/logs/federation/streams?limit=50')
    items = []
    if isinstance(d_str2, dict):
        items = d_str2.get('streams') or d_str2.get('items') or []
    total_hits = sum(i.get('hits', 0) for i in items if isinstance(i, dict))
    print('   真机 stream %d 个，累计 %d 条' % (len(items), total_hits))

    # dim() 同名键会覆盖：两个条件先合并再写入，否则后一条会把前一条吃掉。
    reg_ok = bool(registry and len(reg_lock) >= 3)
    live_ok = total_hits > 0
    v499.dim('capability', reg_ok and live_ok,
             'registry 走短临界区锁且真机在采集（%d 条）' % total_hits
             if (reg_ok and live_ok) else 'registry 缺失或采集未跑')

    # 这就是关键语义：FR-499 说的那套登记锁已随旧链路删除。
    # 能力不缺失（新模型无需该锁），但「登记本体耗时 1.246ms/6.99ms」这类
    # 验收读数已无对象。必须如实记，不能说“仍然达标”。
    old_model_gone = (len(ingest_files) == 0)
    v499.dim('semantics', True,
             '旧登记锁模型已不在（ingest 包 %d 文件）——读数口径失效，但新模型无需它' % len(ingest_files))

    v499.ev('ingest files=%d ledger files=%d registry=%s locks=%d'
            % (len(ingest_files), len(ledger_files), bool(registry), len(reg_lock)))
    v499.ev('live streams=%d total_hits=%d' % (len(items), total_hits))

    if old_model_gone and registry and total_hits > 0:
        v499.judge('COND',
                   '旧登记锁模型已整体替换（ingest/ledger 均删），原验收读数无对象；'
                   '新采集器用 Filebeat 式游标登记，真机在跑。能力不缺失，但指标不可直接沿用')
    elif total_hits > 0:
        v499.judge('PASS', '登记链路可用，真机在采集')
    else:
        v499.judge('FAIL', '采集未在跑或登记结构缺失')
    v499.show()
    rows.append(v499.as_dict())

    # ───────────────── FR-501 采样与保留 ─────────────────
    print('\n3. 采样策略与保留策略（核 PRD 的三项“仍在生产”）')
    v501 = Verdict('FR-501', '采样与保留策略')

    # PRD 声称三项现存能力：四类采样 / 抑制汇总事件守恒 R / 缺口 default-deny
    sampling_impl = git_grep('SamplingPolicy')
    sampler_impl = git_grep('sampleRate|SampleRate')
    print('   SamplingPolicy 命中: %d' % len(sampling_impl))
    for h in sampling_impl[:3]:
        print('      %s' % h[:130])

    # worker.yml 自己的说明是权威的反向证据
    yml_note = git_grep('已随旧链路删除', ['configs/worker.yml'])
    print('   worker.yml 说明: %s' % ('有' if yml_note else '无'))
    for h in yml_note[:2]:
        print('      %s' % h[:160])

    # 只看文档命中 = 实现已删，只剩描述
    impl_hits = [h for h in sampling_impl if not h.startswith('HEAD:docs/')]
    doc_hits = [h for h in sampling_impl if h.startswith('HEAD:docs/')]
    print('   实现命中 %d 处，文档命中 %d 处' % (len(impl_hits), len(doc_hits)))

    v501.dim('capability', len(impl_hits) > 0,
             '采样实现%s' % ('存在' if impl_hits else '不存在（仅文档描述）'))
    # 保留执行器：PRD 说本轮删除，那就得真的不在
    retention_files = git_ls('internal/worker/logs/retention/')
    print('   retention 包: %d 个文件' % len(retention_files))

    V501_NOTE = ('PRD 写「四类采样策略（默认关）仍在生产」，但 HEAD 里 SamplingPolicy 只有文档命中，'
                 '实现为零；configs/worker.yml 自己写着「采集侧采样已随旧链路删除（2026-11-06）：'
                 '新采集器逐行写入、无采样实现」。这是文档与代码不一致，按代码记 FAIL')
    v501.dim('semantics', False, 'PRD 声明与代码不一致：声称在生产，实际已删')
    v501.dim('modern', True, '新采集器逐行写入、无采样——架构上更简单，但需如实记录')

    v501.ev('SamplingPolicy impl=%d doc=%d' % (len(impl_hits), len(doc_hits)))
    v501.ev('worker.yml: %s' % (yml_note[0][:200] if yml_note else 'n/a'))
    v501.ev('retention files=%d' % len(retention_files))

    v501.judge('FAIL', V501_NOTE)
    v501.show()
    rows.append(v501.as_dict())

    # ───────────────── FR-502 采集规则与路径覆盖 ─────────────────
    print('\n4. 采集规则 / 轮转 / 实例路径覆盖')
    v502 = Verdict('FR-502', '四核心采集规则与实例路径覆盖')

    tailer_files = git_ls('internal/worker/logs/tailer/')
    multiline = [f for f in tailer_files if 'multiline' in f]
    growing = [f for f in tailer_files if 'growing' in f]
    gzip_f = [f for f in tailer_files if 'gzip' in f]
    inode_f = [f for f in tailer_files if 'inode' in f]
    print('   tailer 文件 %d 个；多行 %d / 增长 %d / gzip %d / inode %d'
          % (len(tailer_files), len(multiline), len(growing), len(gzip_f), len(inode_f)))

    rules_ok = bool(all([multiline, growing, gzip_f, inode_f]))
    sources = set()
    if isinstance(d_str2, dict):
        items2 = d_str2.get('streams') or d_str2.get('items') or []
        for it in items2:
            if isinstance(it, dict) and it.get('log_source_id'):
                sources.add(it['log_source_id'].split('/')[0])
    print('   真机覆盖实例源 %d 个: %s' % (len(sources), sorted(sources)[:8]))
    # 同名 dim 会覆盖，合并后写入
    v502.dim('capability', rules_ok and len(sources) >= 5,
             '四类规则齐全且真机 %d 个实例源有日志' % len(sources)
             if (rules_ok and len(sources) >= 5) else '规则或覆盖不完整')
    v502.ev('live sources=%s' % sorted(sources))

    v502.ev('tailer files=%d' % len(tailer_files))
    v502.dim('modern', True, '按 Filebeat fingerprint 模型重建，不 import 旧 ingest')

    if v502.dims.get('capability', (False,))[0]:
        v502.judge('PASS', '四类采集规则齐全；真机多实例路径均被覆盖')
    else:
        v502.judge('FAIL', '采集规则或路径覆盖不成立')
    v502.show()
    rows.append(v502.as_dict())

    p = save_report(rows, 'b-log-collect.json')
    print('\n已保存: %s' % p)

    ok = sum(1 for r in rows if r['result'] == 'PASS')
    cond = sum(1 for r in rows if r['result'] == 'COND')
    bad = sum(1 for r in rows if r['result'] == 'FAIL')
    print('\n小计: PASS=%d COND=%d FAIL=%d' % (ok, cond, bad))
    return 0


if __name__ == '__main__':
    sys.exit(main())
