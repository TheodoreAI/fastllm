//go:build !windows && !darwin && !linux

package terminal

func start(cols, rows int, workDir string) (Session, error) {
	return nil, ErrUnsupported
}
