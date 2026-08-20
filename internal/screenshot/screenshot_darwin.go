//go:build darwin

package screenshot

/*
#cgo LDFLAGS: -framework ApplicationServices -framework ImageIO -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <ImageIO/ImageIO.h>
#include <stdlib.h>

// findWindowID scans on-screen windows for one whose owner (process) name
// or window title matches wantedTitle, returning its CGWindowID or 0 if
// none matches. fastllm's Wails window sets both to appTitle (see
// main.go), so either match is fine.
static CGWindowID findWindowID(const char *wantedTitleC) {
    CFStringRef wantedTitle = CFStringCreateWithCString(NULL, wantedTitleC, kCFStringEncodingUTF8);
    CGWindowID result = 0;

    CFArrayRef windowList = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly, kCGNullWindowID);
    if (windowList != NULL) {
        CFIndex count = CFArrayGetCount(windowList);
        for (CFIndex i = 0; i < count; i++) {
            CFDictionaryRef info = (CFDictionaryRef)CFArrayGetValueAtIndex(windowList, i);
            CFStringRef ownerName = (CFStringRef)CFDictionaryGetValue(info, kCGWindowOwnerName);
            CFStringRef windowName = (CFStringRef)CFDictionaryGetValue(info, kCGWindowName);

            int ownerMatch = ownerName != NULL &&
                CFStringCompare(ownerName, wantedTitle, kCFCompareCaseInsensitive) == kCFCompareEqualTo;
            int nameMatch = windowName != NULL &&
                CFStringCompare(windowName, wantedTitle, kCFCompareCaseInsensitive) == kCFCompareEqualTo;

            if (ownerMatch || nameMatch) {
                CFNumberRef num = (CFNumberRef)CFDictionaryGetValue(info, kCGWindowNumber);
                CGWindowID wid = 0;
                if (num != NULL) {
                    CFNumberGetValue(num, kCFNumberIntType, &wid);
                }
                if (wid != 0) {
                    result = wid;
                    break;
                }
            }
        }
        CFRelease(windowList);
    }
    CFRelease(wantedTitle);
    return result;
}

// capturePNG renders the given window to a PNG and hands the caller a
// malloc'd buffer (freed on the Go side) plus its length. Returns 0 on
// success, a negative code identifying which step failed otherwise —
// most commonly this fails when the host process lacks the "Screen
// Recording" permission macOS requires for CGWindowListCreateImage, even
// when capturing the calling app's own window.
static int capturePNG(CGWindowID wid, unsigned char **outData, size_t *outLen) {
    CGImageRef image = CGWindowListCreateImage(
        CGRectNull, kCGWindowListOptionIncludingWindow, wid, kCGWindowImageBoundsIgnoreFraming);
    if (image == NULL) {
        return -1;
    }

    CFMutableDataRef data = CFDataCreateMutable(NULL, 0);
    CGImageDestinationRef dest = CGImageDestinationCreateWithData(data, CFSTR("public.png"), 1, NULL);
    if (dest == NULL) {
        CGImageRelease(image);
        CFRelease(data);
        return -2;
    }

    CGImageDestinationAddImage(dest, image, NULL);
    bool ok = CGImageDestinationFinalize(dest);
    CFRelease(dest);
    CGImageRelease(image);
    if (!ok) {
        CFRelease(data);
        return -3;
    }

    CFIndex len = CFDataGetLength(data);
    unsigned char *buf = malloc((size_t)len);
    CFDataGetBytes(data, CFRangeMake(0, len), buf);
    CFRelease(data);

    *outData = buf;
    *outLen = (size_t)len;
    return 0;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func captureWindow(title string) ([]byte, error) {
	cTitle := C.CString(title)
	defer C.free(unsafe.Pointer(cTitle))

	wid := C.findWindowID(cTitle)
	if wid == 0 {
		return nil, fmt.Errorf("window %q not found", title)
	}

	var outData *C.uchar
	var outLen C.size_t
	if ret := C.capturePNG(wid, &outData, &outLen); ret != 0 {
		return nil, fmt.Errorf(
			"capture window %q failed (code %d) — fastllm may need \"Screen Recording\" "+
				"permission under System Settings > Privacy & Security", title, int(ret))
	}
	defer C.free(unsafe.Pointer(outData))

	return C.GoBytes(unsafe.Pointer(outData), C.int(outLen)), nil
}
