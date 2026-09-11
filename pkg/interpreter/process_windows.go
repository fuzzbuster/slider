//go:build windows

package interpreter

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func nativeProcessName() string {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	entry := windows.ProcessEntry32{
		Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{})),
	}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return ""
	}

	pid := uint32(os.Getpid())
	for {
		if entry.ProcessID == pid {
			return windows.UTF16ToString(entry.ExeFile[:])
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return ""
		}
	}
}
