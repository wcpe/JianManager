package tailer

import "sort"

// sortStrings 按字典序排序（局部包装，避免与包内其它排序需求混淆）。
func sortStrings(s []string) {
	sort.Strings(s)
}
