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
  init     Initialize the panel and print initial credentials once
  info     Print the panel URL and administrator username
  reset-admin  Reset administrator credentials and revoke sessions
  set-host <IP|domain>  Change the public host and print the new panel URL
  set-master <URL>     Switch a node to a verified master without rejoining
  server add <name>    Issue a one-time join command for a new node (master only)
  server list          List servers with status, version and last heartbeat
  server remove <id>   Remove a node: revoke its key and drop it from subscriptions
  join     Register this machine with a master panel
  leave    Clear the local node identity
  serve    Run the panel or the agent (see 'boxpanel serve --help')
  apply    Regenerate, validate and apply sing-box configuration
  singbox  Install or inspect the managed sing-box binary
  system   Manage installer firewall rules and enable BBR
  backup   Create an encrypted database and certificate backup
  restore <file>  Restore a backup and reapply configuration (Linux/systemd)
  migrate  Receive a master migration on a fresh Linux/systemd host (installer)
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
	if args[0] == "backup" || args[0] == "restore" || args[0] == "restore-staged" {
		return runBackup(args[0], args[1:], stdout, stderr)
	}
	if args[0] == "join" || args[0] == "leave" {
		return runAgentCommand(args[0], args[1:], stdout, stderr)
	}
	if args[0] == "serve" {
		return runServe(args[1:], stdout, stderr)
	}
	if args[0] == "apply" {
		return runApply(args[1:], stdout, stderr)
	}
	if args[0] == "singbox" {
		return runSingbox(args[1:], stdout, stderr)
	}
	if args[0] == "system" {
		return runSystem(args[1:], stdout, stderr)
	}
	if args[0] == "server" {
		return runServer(args[1:], stdout, stderr)
	}
	if args[0] == "set-host" {
		return runSetHost(args[1:], stdout, stderr)
	}
	if args[0] == "set-master" {
		return runSetMaster(args[1:], stdout, stderr)
	}
	if args[0] == "migrate" {
		return runMigration(args[1:], stdout, stderr)
	}
	if args[0] == "init" || args[0] == "info" || args[0] == "reset-admin" {
		return runConfigCommand(args[0], args[1:], stdout, stderr)
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
