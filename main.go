package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	program := filepath.Base(os.Args[0])
	switch {
	case program == "secd":
		serverMain()
	case roleFromProgram(program) != "":
		daemonMain()
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown Go entrypoint %q; use secd or sec-*d\n", program)
		os.Exit(1)
	}
}
