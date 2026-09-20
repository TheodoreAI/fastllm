// Command cli runs fastllm as a standalone, autonomous agent harness from the terminal.
package main

import (
	"os"

	"fastllm/internal/harness"
	"fastllm/internal/legacystore"
)

func main() {
	// Installing the opener here, rather than inside internal/harness, keeps the
	// engine free of a SQLite dependency and lets this binary read the database
	// that used to back the web frontend.
	harness.OpenLegacyConversations = legacystore.Installer()
	os.Exit(harness.RunCLI(os.Args[1:]))
}
