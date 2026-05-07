package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

type appError struct {
	msg  string
	code int
}

func (e appError) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return appError{msg: fmt.Sprintf(format, args...), code: code}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var ae appError
		if errors.As(err, &ae) {
			if ae.code == 0 {
				os.Exit(0)
			}
			fmt.Fprintln(os.Stderr, "error:", ae.msg)
			os.Exit(ae.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printHelp()
		return nil
	}
	cmd := aliasCommand(args[0])
	rest := args[1:]
	switch cmd {
	case "init-db":
		return cmdInitDB(rest)
	case "security-upsert":
		return cmdSecurityUpsert(rest)
	case "position-upsert":
		return cmdPositionUpsert(rest)
	case "sec-watch":
		return cmdSecWatch(rest)
	case "sec-fetch":
		return cmdSecFetch(rest)
	case "xbrl-parse":
		return cmdXBRLParse(rest)
	case "broker-reconcile":
		return cmdBrokerReconcile(rest)
	case "plan":
		return cmdPlan(rest)
	case "value":
		return cmdValue(rest)
	case "gate":
		return cmdGate(rest)
	case "order-stage":
		return cmdOrderStage(rest)
	case "broker-submit":
		return cmdBrokerSubmit(rest)
	case "set-mode":
		return cmdSetMode(rest)
	case "trading-halt":
		return cmdTradingHalt(rest)
	case "status":
		return cmdStatus(rest)
	case "report":
		return cmdReport(rest)
	case "notify":
		return cmdNotify(rest)
	case "universe-build":
		return cmdUniverseBuild(rest)
	case "ci-seed":
		return cmdCISeed(rest)
	case "facts-companyfacts":
		return fail(2, "%s is reserved for a future companyfacts fallback; use watch, pull, and xbrl for the Go ingestion path", cmd)
	default:
		return fail(2, "unknown command: %s", args[0])
	}
}

func aliasCommand(cmd string) string {
	switch cmd {
	case "init":
		return "init-db"
	case "sym":
		return "security-upsert"
	case "pos":
		return "position-upsert"
	case "watch":
		return "sec-watch"
	case "pull":
		return "sec-fetch"
	case "comp":
		return "facts-companyfacts"
	case "xbrl":
		return "xbrl-parse"
	case "univ":
		return "universe-build"
	case "recon":
		return "broker-reconcile"
	case "plan":
		return "plan"
	case "value":
		return "value"
	case "gate":
		return "gate"
	case "stage":
		return "order-stage"
	case "send":
		return "broker-submit"
	case "mode":
		return "set-mode"
	case "halt":
		return "trading-halt"
	case "stat":
		return "status"
	case "ping":
		return "notify"
	default:
		return cmd
	}
}

func printHelp() {
	fmt.Println("usage: sec <command> [options]")
	fmt.Println()
	fmt.Println("commands: init sym pos watch pull comp xbrl univ recon plan value gate stage send mode halt stat report ping")
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return appError{msg: "help requested", code: 0}
		}
		return err
	}
	return nil
}
