package platform

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	findWindow    = user32.NewProc("FindWindowW")
	flashWindowEx = user32.NewProc("FlashWindowEx")
)

// FlashWindowEx flags.
const (
	flashwTray      = 0x00000002 // flash the taskbar button
	flashwTimerNoFG = 0x0000000C // keep flashing until the window comes to the foreground
)

// flashWInfo mirrors the Win32 FLASHWINFO struct.
type flashWInfo struct {
	cbSize    uint32
	hwnd      uintptr
	dwFlags   uint32
	uCount    uint32
	dwTimeout uint32
}

// FlashTaskbar makes the taskbar button of the top-level window titled
// windowTitle flash until the user brings the window to the foreground —
// the standard Windows "something needs your attention" signal.
func FlashTaskbar(windowTitle string) error {
	title, err := syscall.UTF16PtrFromString(windowTitle)
	if err != nil {
		return err
	}
	hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return fmt.Errorf("window %q not found", windowTitle)
	}
	info := flashWInfo{hwnd: hwnd, dwFlags: flashwTray | flashwTimerNoFG}
	info.cbSize = uint32(unsafe.Sizeof(info))
	flashWindowEx.Call(uintptr(unsafe.Pointer(&info)))
	return nil
}
