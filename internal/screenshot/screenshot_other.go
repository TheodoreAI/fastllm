//go:build !windows && !darwin

package screenshot

func captureWindow(title string) ([]byte, error) {
	return nil, ErrUnsupported
}
