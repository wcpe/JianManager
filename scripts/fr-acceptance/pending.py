#!/usr/bin/env python3
"""盘点：82 条新增 FR 里哪些已验收、哪些还没。

已验收清单来自验收脚本里实际断言过的 FR 号。
"""
import re
import glob
import os

REPO = '/home/wxys233/projects/JianManager'

# 从验收脚本里提取实际引用过的 FR 号
# 注意：batch_probe.py 的 FR 号在 CASES 元组里，也要算
covered = set()
for pat in ('p*.py', 'batch_probe.py'):
    for f in glob.glob(os.path.join(REPO, 'scripts/fr-acceptance', pat)):
        txt = open(f, encoding='utf-8').read()
        for m in re.findall(r'FR-(\d+)', txt):
            covered.add(int(m))

# 82 条新增 FR（相对 v0.23.0）：FR-472~486, 489~503, 505~512, 513~545, 546~556
added = []
for a, b in [(472, 486), (489, 503), (505, 512), (513, 545), (546, 556)]:
    added += list(range(a, b + 1))

# 排除从来没有主表条目的号
missing_nums = {487, 488, 504}
added = [n for n in added if n not in missing_nums]

pend = [n for n in added if n not in covered]
print('新增 FR 总数（不含从未使用的号）:', len(added))
print('已验收（脚本里断言过）:', len([n for n in added if n in covered]))
print('未验收:', len(pend))
print()
# 按号段分组输出
from itertools import groupby
out = []
nums = sorted(pend)
i = 0
while i < len(nums):
    j = i
    while j + 1 < len(nums) and nums[j + 1] == nums[j] + 1:
        j += 1
    out.append((nums[i], nums[j]))
    i = j + 1
for a, b in out:
    if a == b:
        print('  FR-%d' % a)
    else:
        print('  FR-%d ~ FR-%d  (%d 条)' % (a, b, b - a + 1))
