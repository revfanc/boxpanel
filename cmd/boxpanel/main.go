package main

import (
	"fmt"
	"io"
	"os"
)

// version can be set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

const usage = `Usage: boxpanel <command>

Commands:
  serve    Serve the panel over HTTP (see 'boxpanel serve --help')
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
	if args[0] == "serve" {
		return runServe(args[1:], stdout, stderr)
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
