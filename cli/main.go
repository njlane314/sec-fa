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
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "init":
		return cmdInitDB(rest)
	case "sym":
		return cmdSecurityUpsert(rest)
	case "pos":
		return cmdPositionUpsert(rest)
	case "watch":
		return cmdSecWatch(rest)
	case "pull":
		return cmdSecFetch(rest)
	case "xbrl":
		return cmdXBRLParse(rest)
	case "univ":
		return cmdUniverseBuild(rest)
	case "recon":
		return cmdBrokerReconcile(rest)
	case "plan":
		return cmdPlan(rest)
	case "value":
		return cmdValue(rest)
	case "gate":
		return cmdGate(rest)
	case "stage":
		return cmdOrderStage(rest)
	case "send":
		return cmdBrokerSubmit(rest)
	case "mode":
		return cmdSetMode(rest)
	case "halt":
		return cmdTradingHalt(rest)
	case "stat":
		return cmdStatus(rest)
	case "report":
		return cmdReport(rest)
	case "ping":
		return cmdNotify(rest)
	default:
		return fail(2, "unknown command: %s", args[0])
	}
}

func printHelp() {
	fmt.Println("usage: sec <command> [options]")
	fmt.Println()
	fmt.Println("commands: init sym pos watch pull xbrl univ recon plan value gate stage send mode halt stat report ping")
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
