#!/usr/bin/env python3
"""提取指定号段的 FR 原文（标题 + 状态 + spec 落点），供验收时逐条比对。"""
import json
import re
import sys

PRD = '/home/wxys233/projects/JianManager/docs/PRD.md'


def load(path=PRD):
    txt = open(path, encoding='utf-8').read()
    rows = {}
    for fr, n, rest in re.findall(r'^\|\s*(FR-(\d+))\s*\|(.*?)$', txt, re.M):
        cells = [c.strip() for c in rest.split('|')]
        cells = [c for c in cells if c]
        rows[int(n)] = {
            'fr': fr,
            'title': cells[0] if cells else '',
            'priority': cells[1] if len(cells) > 2 else '',
            'status': cells[-1] if cells else '',
        }
    return rows


def spec_of(title):
    m = re.search(r'`(docs/specs/[^`]+)`', title)
    if m:
        return m.group(1)
    m = re.search(r'\[规格\]\[?s?(\d+)\]?', title)
    if m:
        return 's' + m.group(1)
    return ''


if __name__ == '__main__':
    groups = sys.argv[1:] or ['472-486', '489-512']
    rows = load()
    for g in groups:
        a, b = [int(x) for x in g.split('-')]
        print('=== FR-%d ~ FR-%d ===' % (a, b))
        for n in range(a, b + 1):
            if n not in rows:
                continue
            r = rows[n]
            st = r['status']
            mark = re.match(r'^(✅|❌|⏸️|⏸|🔨|📋|⚠️)', st)
            t = r['title'][:74]
            sp = spec_of(r['title'])
            print('%s %s %s' % (r['fr'], (mark.group(1) if mark else '?'), t))
            if sp:
                print('       spec: %s' % sp)
        print()
