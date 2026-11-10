#!/usr/bin/env python3
"""日志核心 FR 的实现存在性验收（FR-511 / FR-512）

这两条的 PRD 状态写「✅ 已交付」，但验收要回答的是：
**声明的实现对象到底存不存在**。分两个层次：

  A. 配置/常量层：有没有 PUSH_ONLY 这个模式名与校验
  B. 实际端点层：有没有真正在听的 HTTP 接收服务

两者不是一回事——只有 A 没 B，等于“声明了一个接收端但从没实现”。
"""
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import Verdict, save_report

REPO = '/home/wxys233/projects/JianManager'


def grep(pattern, paths, glob='*.go', exclude_tests=True):
    cmd = ['grep', '-rn', pattern, '--include=' + glob] + paths
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    lines = [l for l in r.stdout.splitlines() if l]
    if exclude_tests:
        lines = [l for l in lines if '_test.go' not in l]
    return lines


def main():
    rows = []
    print('=== 日志核心 FR 实现存在性验收 ===\n')

    # ── FR-511：Worker 内置 HTTP 日志接收入口 ──
    print('FR-511 Worker 内置 HTTP 日志接收入口')
    # A. 模式名与校验
    mode_hits = grep('PUSH_ONLY', ['internal/worker/'])
    print('  A. PUSH_ONLY 模式名/校验: %d 处' % len(mode_hits))
    for h in mode_hits[:3]:
        print('     ' + h.split(REPO + '/')[-1][:110])

    # B. 实际 HTTP 接收端点：找“Worker 作为服务端接收日志”的实现
    #    /insert/jsonline 只出现在 vlsup/client.go 里（Worker 作为**客户端**推给 VL）
    client_hits = grep('insert/jsonline', ['internal/worker/logs/vlsup/'])
    print('  /insert/jsonline 在 vlsup（客户端方向）: %d 处' % len(client_hits))

    #    找 worker 侧真正的 HTTP server 监听（除 ws 之外）
    server_hits = grep('ListenAndServe', ['apps/worker/'])
    print('  worker main 里的 ListenAndServe: %d 处' % len(server_hits))
    for h in server_hits[:3]:
        print('     ' + h.split(REPO + '/')[-1][:110])

    #    找是否有 ingest 相关的 HTTP handler
    ingest_handler = grep('ingest.*Handler\\|IngestHandler\\|pushIngest', ['internal/worker/'])
    print('  ingest HTTP handler: %d 处' % len(ingest_handler))

    has_mode = len(mode_hits) > 0
    has_endpoint = len(ingest_handler) > 0

    v = Verdict('FR-511', 'Worker 内置 HTTP 日志接收入口')
    v.dim('capability', has_mode,
          'PUSH_ONLY 模式名与配置校验存在（%d 处）' % len(mode_hits))
    v.dim('semantics', has_endpoint,
          'HTTP 接收端点%s' % ('已实现' if has_endpoint else '**不存在**'))
    v.ev('PUSH_ONLY 引用 %d 处；worker ListenAndServe %d 处（均为 ws/tunnel）；'
         'ingest Handler %d 处' % (len(mode_hits), len(server_hits), len(ingest_handler)))

    if has_endpoint:
        v.judge('PASS', '接收端已实现')
    elif has_mode:
        v.judge('FAIL',
                '只声明了 PUSH_ONLY 模式与校验（config.go），但没有任何 HTTP 接收端点实现。'
                '所有 /insert/jsonline 相关代码都在 vlsup/client.go——那是 Worker 作为**客户端**'
                '推给 VL 的方向，不是接收方向。PRD 标「已交付」与实际不符。')
    else:
        v.judge('FAIL', '实现完全不存在')
    v.show()
    rows.append(v.as_dict())

    # ── FR-512：三类来源统一抽象 ──
    print('\nFR-512 采集模式对齐官方（三类来源统一抽象）')
    file_hits = grep('FILE_PRIMARY', ['internal/worker/logs/'])
    stdio_hits = grep('STDIO_PRIMARY', ['internal/worker/logs/'])
    # PUSH 类在 logs/ 下是否存在真实实现（不只是常量）
    push_logs = grep('PUSH_ONLY', ['internal/worker/logs/'])
    print('  FILE_PRIMARY: %d 处' % len(file_hits))
    print('  STDIO_PRIMARY: %d 处' % len(stdio_hits))
    print('  PUSH_ONLY（logs 包内）: %d 处' % len(push_logs))

    v2 = Verdict('FR-512', '按文件/进程/HTTP 三类来源统一抽象')
    v2.dim('capability', len(file_hits) > 0 and len(stdio_hits) > 0,
           '文件与进程两类有实现（FILE %d / STDIO %d）' % (len(file_hits), len(stdio_hits)))
    v2.dim('semantics', len(push_logs) > 0,
           'HTTP(push) 类在 logs 包内%s' % ('有实现' if push_logs else '**无实现**'))
    v2.ev('FILE=%d STDIO=%d PUSH(logs)=%d' % (len(file_hits), len(stdio_hits), len(push_logs)))

    if len(push_logs) > 0:
        v2.judge('PASS', '三类齐全')
    else:
        v2.judge('COND',
                 '文件与进程两类已实现且真实在用（生产 19 源全 FILE_PRIMARY）；'
                 '但 HTTP(push) 类依赖 FR-511 的接收端点，而该端点未实现——'
                 '所以「三类统一抽象」实际只有两类。')
    v2.show()
    rows.append(v2.as_dict())

    save_report(rows, 'log-core-impl.json')


if __name__ == '__main__':
    main()
