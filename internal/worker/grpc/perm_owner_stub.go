//go:build !unix

package grpc

// Windows 及其他非 Unix：属主/组留空，以 readable/writable 为准（FR-373）。
