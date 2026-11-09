package main

import (
	"fmt"
	"io"
	"os"

	// Include the frontend assets; HTTP serving is added in T0.2.
	_ "github.com/revfanc/boxpanel/web"
)

// version can be set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

const usage = `Usage: boxpanel <command>

Commands:
  version  Print the boxpanel version
  help     Show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "boxpanel: unexpected arguments; run 'boxpanel help' for usage")
		return 2
	}

	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "boxpanel %s\n", version)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "boxpanel: unknown command %q; run 'boxpanel help' for usage\n", args[0])
		return 2
	}
	return 0
}
