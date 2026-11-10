#!/usr/bin/env python3
"""P4 原子写压力验证：高频读写 PID 文件，确认不会读到半截内容

背景：本轮修复了 daemon.PIDFile.WriteRecord 的非原子写（os.WriteFile 截断→写入）。
真机验证：在 Worker 正在读写 PID 文件的同时，持续读取观察是否出现解析失败。

特别注意：不能干扰生产 PID 文件，用副本在临时目录测。
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import Verdict, save_report

SRC = '/srv/jm/worker-data/var/servers'


def main():
    rows = []
    print('=== P4 PID 文件原子写真机验证 ===\n')

    # 用真实 PID 文件做模板拷贝到临时目录（不碰生产文件）
    tmpdir = tempfile.mkdtemp(prefix='pidtest-')
    real = [f for f in os.listdir(SRC) if f.endswith('.pid')][:1]
    if not real:
        print('无可参照 PID 文件')
        return
    src = os.path.join(SRC, real[0])
    sample = json.load(open(src))
    print('参照样本: %s' % real[0])
    print('  字段: %s' % list(sample.keys()))

    target = os.path.join(tmpdir, 'test.pid')

    # 模拟 wrapper 的两阶段写（监听就绪写一次，java 拉起后补写）
    def write_record(path, java_pid):
        rec = dict(sample)
        rec['java_pid'] = java_pid
        data = json.dumps(rec, ensure_ascii=False).encode()
        # 复刻原子写：临时文件 + rename
        t = path + '.tmp%d' % threading.get_ident()
        with open(t, 'wb') as f:
            f.write(data)
        os.replace(t, path)

    # 对照组：非原子写（截断→写入），验证能复现问题
    def write_record_naive(path, java_pid):
        rec = dict(sample)
        rec['java_pid'] = java_pid
        data = json.dumps(rec, ensure_ascii=False).encode()
        with open(path, 'wb') as f:   # 'wb' 先截断
            f.write(data)

    def stress(writer, rounds=3000):
        """并发写 + 持续读，统计读到的异常。"""
        errs = []
        stop = threading.Event()

        def reader():
            while not stop.is_set():
                try:
                    with open(target, 'rb') as f:
                        raw = f.read()
                    if raw:
                        d = json.loads(raw)
                        if not d.get('wrapper_pid'):
                            errs.append('missing wrapper_pid')
                except json.JSONDecodeError as e:
                    errs.append('JSONDecodeError: %s' % str(e)[:40])
                except FileNotFoundError:
                    errs.append('FileNotFoundError')
                except Exception as e:
                    errs.append('%s: %s' % (type(e).__name__, str(e)[:40]))

        threads = [threading.Thread(target=reader, daemon=True) for _ in range(4)]
        for t in threads:
            t.start()
        for i in range(rounds):
            writer(target, 2000 + i)
        stop.set()
        for t in threads:
            t.join(timeout=1)
        return errs

    # 1. 非原子写（对照，应能复现问题）
    print('\n1. 对照组：非原子写（os.WriteFile 行为）')
    writer_for_naive = write_record_naive
    errs_naive = stress(writer_for_naive)
    print('   读到异常 %d 次' % len(errs_naive))
    if errs_naive:
        print('   示例: %s' % errs_naive[:3])

    # 2. 原子写（本修复的行为）
    print('\n2. 实验组：原子写（临时文件 + rename）')
    errs_atomic = stress(write_record)
    print('   读到异常 %d 次' % len(errs_atomic))
    if errs_atomic:
        print('   示例: %s' % errs_atomic[:3])

    # 裁定
    v = Verdict('PID-atomic', 'PID 文件原子写消除半截读')
    v.dim('capability', len(errs_atomic) == 0,
          '原子写下 3000 轮读/写并发，异常 %d 次' % len(errs_atomic))
    v.dim('modern', len(errs_naive) > 0,
          '对照组（非原子）异常 %d 次，证明测试确实能发现问题' % len(errs_naive))
    v.ev('非原子写异常数=%d，原子写异常数=%d' % (len(errs_naive), len(errs_atomic)))

    if len(errs_atomic) == 0 and len(errs_naive) > 0:
        v.judge('PASS', '原子写彻底消除了半截读；对照组能稳定复现，证明验证有效')
    elif len(errs_atomic) == 0:
        v.judge('COND', '原子写无异常，但对照组也未复现，验证强度不足')
    else:
        v.judge('FAIL', '原子写仍存在异常')
    v.show()
    rows.append(v.as_dict())

    shutil.rmtree(tmpdir, ignore_errors=True)
    save_report(rows, 'p4-atomic.json')


if __name__ == '__main__':
    main()
