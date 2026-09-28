//go:build windows

package stage

import (
	"syscall"
	"unsafe"
)

var procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")

// Free returns the bytes available to the calling user in dir.
func Free(dir string) (uint64, bool) {
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var avail, total, totalFree uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return 0, false
	}
	return avail, true
}
