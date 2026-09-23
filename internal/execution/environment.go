package execution

import (
	"os"
	"strings"
)

func processEnvironment(user bool) []string {
	if user {
		return os.Environ()
	}
	return SanitizedEnvironment()
}

// SanitizedEnvironment reduces accidental credential inheritance; this is not
// isolation from credentials on disk or from secrets with unrecognized names.
func SanitizedEnvironment() []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		essential := false
		switch key {
		case "PATH", "HOME", "USER", "USERPROFILE", "LOGNAME", "SHELL", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "COLORTERM", "GOROOT", "GOPATH", "GOBIN", "GOPROXY", "GONOSUMDB", "GONOPROXY", "GOPRIVATE", "CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "NODE_PATH", "NVM_DIR", "SYSTEMROOT", "WINDIR", "PROGRAMFILES", "PROGRAMFILES(X86)", "APPDATA", "LOCALAPPDATA", "COMSPEC", "PATHEXT":
			essential = true
		}
		sensitive := false
		for _, word := range []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "PASSWD", "AUTH", "CREDENTIAL", "PRIVATE", "CERT", "SIGNING"} {
			if strings.Contains(key, word) {
				sensitive = true
				break
			}
		}
		if essential || !sensitive {
			result = append(result, entry)
		}
	}
	return result
}
