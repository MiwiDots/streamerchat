package platform

import (
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	user32           = syscall.NewLazyDLL("user32.dll")
	getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	getWindowLong    = user32.NewProc("GetWindowLongW")
	setWindowLong    = user32.NewProc("SetWindowLongW")
	setWindowPos     = user32.NewProc("SetWindowPos")
	showWindow       = user32.NewProc("ShowWindow")
	setWindowText    = user32.NewProc("SetWindowTextW")
)

const (
	gwlStyle   = ^uintptr(16 - 1) // -16 as uintptr
	gwlExStyle = ^uintptr(20 - 1) // -20 as uintptr

	// Window styles to remove
	wsCaption     = 0x00C00000 // title bar
	wsThickFrame  = 0x00040000 // sizing border
	wsSysMenu     = 0x00080000 // system menu
	wsMinimizeBox = 0x00020000
	wsMaximizeBox = 0x00010000

	// Keep these
	wsVisible  = 0x10000000
	wsPopup    = 0x80000000
	wsBorder   = 0x00800000

	// Extended styles
	wsExToolWindow  = 0x00000080 // hide from taskbar
	wsExAppWindow   = 0x00040000 // show on taskbar
	wsExWindowEdge  = 0x00000100

	// SetWindowPos flags
	swpNoMove   = 0x0002
	swpNoSize   = 0x0001
	swpFrameChanged = 0x0020
	swpNoZOrder = 0x0004

	swShow = 5
)

// SetupCleanWindow removes the terminal chrome (tab bar, title bar)
// and makes the console window look like a standalone app.
func SetupCleanWindow() {
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return
	}

	// Set window title
	title, _ := syscall.UTF16PtrFromString("StreamerChat")
	setWindowText.Call(hwnd, uintptr(unsafe.Pointer(title)))

	// Get current style
	style, _, _ := getWindowLong.Call(hwnd, gwlStyle)

	// Keep caption and sizing frame so user can resize/move window
	// Only hide the tab bar via terminal-level settings if possible
	newStyle := style | uintptr(wsThickFrame|wsSysMenu|wsMinimizeBox|wsMaximizeBox)
	setWindowLong.Call(hwnd, gwlStyle, newStyle)

	// Apply changes
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		uintptr(swpNoMove|swpNoSize|swpFrameChanged|swpNoZOrder))

	showWindow.Call(hwnd, uintptr(swShow))
}
