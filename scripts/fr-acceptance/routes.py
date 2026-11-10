#!/usr/bin/env python3
"""从代码提取真实路由（方法 + 路径），避免“用错方法/路径就判 404”的误判。

这个错误我犯了不止一次：
  - /logs/coverage 判 404（真实是 /logs/federation）
  - /business/batch 判 404（真实是 POST，我用了 GET）
所以把路由源作为工具固化下来。
"""
import os
import re
import sys

ROUTER_DIR = '/home/wxys233/projects/JianManager/internal/controlplane/router'

# 匹配 gin 风格注册：rg.POST("/path", ...)，并捕获 receiver 名
PAT = re.compile(r'\b(\w+)\.(GET|POST|PUT|DELETE|PATCH)\(\s*"([^"]+)"')
# 匹配分组： alerts := rg.Group("/alerts")
GROUP_PAT = re.compile(r'(\w+)\s*:=\s*\w+\.Group\(\s*"([^"]+)"')


def collect():
    routes = []
    for fn in sorted(os.listdir(ROUTER_DIR)):
        if not fn.endswith('.go') or fn.endswith('_test.go'):
            continue
        path = os.path.join(ROUTER_DIR, fn)
        try:
            txt = open(path, encoding='utf-8').read()
        except OSError:
            continue
        groups = {m.group(1): m.group(2) for m in GROUP_PAT.finditer(txt)}
        for m in PAT.finditer(txt):
            recv, method, p = m.group(1), m.group(2), m.group(3)
            prefix = groups.get(recv, '')
            line = txt[:m.start()].count('\n') + 1
            routes.append((method, prefix + p, fn, line))
    return routes


if __name__ == '__main__':
    routes = collect()
    if len(sys.argv) > 1:
        kw = sys.argv[1].lower()
        routes = [r for r in routes if kw in r[1].lower()]
    print('共 %d 条路由%s' % (len(routes), '（已过滤）' if len(sys.argv) > 1 else ''))
    for method, p, fn, line in routes:
        print('  %-7s %-45s %s:%d' % (method, p, fn, line))
