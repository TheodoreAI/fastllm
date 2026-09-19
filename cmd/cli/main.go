// Command cli runs fastllm as a standalone, autonomous agent harness from the terminal.
package main

import (
	"os"

	"fastllm/internal/harness"
)

func main() {
	os.Exit(harness.RunCLI(os.Args[1:]))
}
