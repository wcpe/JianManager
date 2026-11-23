//go:build !windows && !darwin && !linux

package keyring

// 其它平台（freebsd / openbsd / plan9 等）没有实现，
// backend 返回 nil 让 Open() 报 ErrUnavailable，调用方据此提示改用 JM_TOKEN。
func backend() Keyring { return nil }
