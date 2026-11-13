package tailer

import "sort"

// sortStrings 按字典序排序（局部包装，避免与包内其它排序需求混淆）。
func sortStrings(s []string) {
	sort.Strings(s)
}

// sortFileStates 按身份键排序位点快照。
//
// 用途：Flush 时保证同样内容两次写出逐字节相同（便于 diff 排障），
// 而不是依赖 map 遍历顺序（Go 的 map 迭代顺序是随机的）。
func sortFileStates(s []FileState) {
	sort.Slice(s, func(i, j int) bool {
		return s[i].Identity.Key() < s[j].Identity.Key()
	})
}
