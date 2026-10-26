package shadowgit

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openCacheLease(name string, create bool) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	disposition := uint32(windows.OPEN_EXISTING)
	var attributes *windows.SecurityAttributes
	if create {
		disposition = windows.CREATE_NEW
		attributes = &windows.SecurityAttributes{InheritHandle: 1}
		attributes.Length = uint32(unsafe.Sizeof(*attributes))
	}
	// Denying write/delete sharing keeps inherited child handles protective
	// even after the parent's LockFileEx range lock is released on exit.
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ, attributes, disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

func cacheLeaseBusy(err error) bool { return errors.Is(err, windows.ERROR_SHARING_VIOLATION) }

func inheritCacheLease(cmd *exec.Cmd, file *os.File) {
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(file.Fd())}}
	// Cmd stores a raw Windows handle; retain the Go file until the command
	// finishes so its finalizer cannot close the handle before inheritance.
	cancel := cmd.Cancel
	cmd.Cancel = func() error {
		defer runtime.KeepAlive(file)
		return cancel()
	}
}
