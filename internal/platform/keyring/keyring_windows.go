//go:build windows

package keyring

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// Windows 实现：直接调 advapi32 的 CredRead/CredWrite/CredDelete。
//
// 不用 cmdkey：它只写不读，无法完成 Get。
// 不用 golang.org/x/sys/windows：避免为三个 API 引入整包依赖。

var (
	advapi32      = syscall.NewLazyDLL("advapi32.dll")
	procCredReadW = advapi32.NewProc("CredReadW")
	procCredWrite = advapi32.NewProc("CredWriteW")
	procCredDel   = advapi32.NewProc("CredDeleteW")
	procCredFree  = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	errNotFound             = 1168 // ERROR_NOT_FOUND
)

// credential 对应 Win32 CREDENTIALW 结构。
// 字段顺序与大小必须与系统定义一致，否则 CredWriteW 会失败。
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type windowsKeyring struct{}

func backend() Keyring { return windowsKeyring{} }

// Set 写入或覆盖凭据。
func (windowsKeyring) Set(key, value string) error {
	k, err := normalizeKey(key)
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(serviceName + "/" + k)
	if err != nil {
		return fmt.Errorf("转换目标名失败: %w", err)
	}
	blob := []byte(value)
	if len(blob) == 0 {
		return fmt.Errorf("凭据值不能为空")
	}
	user, _ := syscall.UTF16PtrFromString("jm")

	cred := credential{
		Type:               credTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}
	ret, _, errCall := procCredWrite.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if ret == 0 {
		return fmt.Errorf("写入 Windows 凭据失败: %v", errCall)
	}
	return nil
}

// Get 读取凭据。
func (windowsKeyring) Get(key string) (string, error) {
	k, err := normalizeKey(key)
	if err != nil {
		return "", err
	}
	target, err := syscall.UTF16PtrFromString(serviceName + "/" + k)
	if err != nil {
		return "", fmt.Errorf("转换目标名失败: %w", err)
	}

	var pcred *credential
	ret, _, errCall := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&pcred)))
	if ret == 0 {
		if isNotFound(errCall) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("读取 Windows 凭据失败: %v", errCall)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(pcred)))

	if pcred == nil || pcred.CredentialBlobSize == 0 {
		return "", ErrNotFound
	}
	blob := unsafe.Slice(pcred.CredentialBlob, pcred.CredentialBlobSize)
	return string(blob), nil
}

// Delete 删除凭据；不存在视为成功。
func (windowsKeyring) Delete(key string) error {
	k, err := normalizeKey(key)
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(serviceName + "/" + k)
	if err != nil {
		return fmt.Errorf("转换目标名失败: %w", err)
	}
	ret, _, errCall := procCredDel.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0)
	if ret == 0 && !isNotFound(errCall) {
		return fmt.Errorf("删除 Windows 凭据失败: %v", errCall)
	}
	return nil
}

// isNotFound 判断 Win32 错误是否为「条目不存在」。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errno, ok := err.(syscall.Errno); ok {
		return int(errno) == errNotFound
	}
	return strings.Contains(err.Error(), "1168")
}
