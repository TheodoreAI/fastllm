//go:build !windows

package terminal

func start(cols, rows int) (Session, error) {
	return nil, ErrUnsupported
}
