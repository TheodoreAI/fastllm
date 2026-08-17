//go:build windows

package terminal

import "github.com/UserExistsError/conpty"

// conptySession adapts *conpty.ConPty to the Session interface — mainly so
// handler.go never imports conpty directly, keeping the Windows-only
// dependency isolated to this file.
type conptySession struct {
	cpty *conpty.ConPty
}

func start(cols, rows int) (Session, error) {
	cpty, err := conpty.Start(
		"powershell.exe -NoLogo",
		conpty.ConPtyDimensions(cols, rows),
	)
	if err != nil {
		return nil, err
	}
	return &conptySession{cpty: cpty}, nil
}

func (s *conptySession) Read(p []byte) (int, error)  { return s.cpty.Read(p) }
func (s *conptySession) Write(p []byte) (int, error) { return s.cpty.Write(p) }
func (s *conptySession) Resize(cols, rows int) error  { return s.cpty.Resize(cols, rows) }
func (s *conptySession) Close() error                 { return s.cpty.Close() }
