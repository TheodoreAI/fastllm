//go:build windows

package screenshot

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"unsafe"

	"golang.org/x/sys/windows"
)

// golang.org/x/sys/windows doesn't wrap FindWindowW, PrintWindow, or the
// GDI bitmap functions needed to capture a window's content, so they're
// declared directly against user32.dll/gdi32.dll — same technique as
// internal/terminal/jobobject_windows.go uses for the Job Object API.

const (
	// PW_RENDERFULLCONTENT (available since Windows 8.1) is required to
	// correctly capture windows that host DirectComposition/hardware-
	// accelerated child surfaces — which is exactly what WebView2 is.
	// Without this flag, PrintWindow against a WebView2-hosting window
	// commonly returns a blank/white capture instead of erroring, which
	// is the kind of silent failure that's easy to miss without actually
	// looking at the output.
	pwRenderFullContent = 0x2

	dibRgbColors  = 0
	biRGB         = 0
	bitmapInfoHdr = 40 // sizeof(BITMAPINFOHEADER)
)

var (
	modUser32 = windows.NewLazySystemDLL("user32.dll")
	modGdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procFindWindowW         = modUser32.NewProc("FindWindowW")
	procGetClientRect       = modUser32.NewProc("GetClientRect")
	procGetDC               = modUser32.NewProc("GetDC")
	procReleaseDC           = modUser32.NewProc("ReleaseDC")
	procPrintWindow         = modUser32.NewProc("PrintWindow")
	procCreateCompatibleDC  = modGdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = modGdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject        = modGdi32.NewProc("SelectObject")
	procDeleteObject        = modGdi32.NewProc("DeleteObject")
	procDeleteDC            = modGdi32.NewProc("DeleteDC")
	procGetDIBits           = modGdi32.NewProc("GetDIBits")
)

// bitmapInfoHeader mirrors BITMAPINFOHEADER — passed by raw pointer to
// GetDIBits, so its layout must match the OS struct exactly.
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func captureWindow(title string) ([]byte, error) {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return nil, err
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return nil, fmt.Errorf("window %q not found", title)
	}

	var rect windows.Rect
	ret, _, err := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return nil, fmt.Errorf("GetClientRect: %w", err)
	}
	width := int(rect.Right - rect.Left)
	height := int(rect.Bottom - rect.Top)
	if width <= 0 || height <= 0 {
		return nil, errors.New("window has no visible client area")
	}

	hdcWindow, _, err := procGetDC.Call(hwnd)
	if hdcWindow == 0 {
		return nil, fmt.Errorf("GetDC: %w", err)
	}
	defer procReleaseDC.Call(hwnd, hdcWindow)

	hdcMem, _, err := procCreateCompatibleDC.Call(hdcWindow)
	if hdcMem == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC: %w", err)
	}
	defer procDeleteDC.Call(hdcMem)

	hBitmap, _, err := procCreateCompatibleBmp.Call(hdcWindow, uintptr(width), uintptr(height))
	if hBitmap == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap: %w", err)
	}
	defer procDeleteObject.Call(hBitmap)

	oldObj, _, _ := procSelectObject.Call(hdcMem, hBitmap)
	defer procSelectObject.Call(hdcMem, oldObj)

	ret, _, err = procPrintWindow.Call(hwnd, hdcMem, pwRenderFullContent)
	if ret == 0 {
		return nil, fmt.Errorf("PrintWindow: %w", err)
	}

	header := bitmapInfoHeader{
		Size:        bitmapInfoHdr,
		Width:       int32(width),
		Height:      int32(-height), // negative = top-down DIB, matches image.RGBA's row order
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
	buf := make([]byte, width*height*4)
	ret, _, err = procGetDIBits.Call(
		hdcMem,
		hBitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&header)),
		dibRgbColors,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits: %w", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// GetDIBits with BI_RGB returns BGRA byte order; image.RGBA wants RGBA.
	for i := 0; i < len(buf); i += 4 {
		b, g, r, a := buf[i], buf[i+1], buf[i+2], buf[i+3]
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, a
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("png encode: %w", err)
	}
	return out.Bytes(), nil
}
