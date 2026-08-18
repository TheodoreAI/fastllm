//go:build !windows

package screenshot

func captureWindow(title string) ([]byte, error) {
	return nil, ErrUnsupported
}
