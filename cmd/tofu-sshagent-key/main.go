package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2

	programName           = "tofu-sshagent-key"
	defaultRecipientsPath = "encryption/recipients.json"
)

type invocation struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	recipients string
	args       []string
}

type command struct {
	run      func(ctx context.Context, inv *invocation) error
	name     string
	synopsis string
	help     string
	nargs    int
}

var commands = []command{
	{
		name:     "key",
		synopsis: "key [--recipients PATH]",
		help: "OpenTofu external key provider (key_provider \"external\"). Writes only protocol JSON to stdout:\n" +
			"the state encryption key, unwrapped with the first enrolled ed25519 key found in ssh-agent.",
		run: runKey,
	},
	{
		name:     "init",
		synopsis: "init [--recipients PATH]",
		help: "Generates a new data key and wraps it for every ed25519 key in ssh-agent.\n" +
			"Refuses to overwrite an existing recipients file.",
		run: runInit,
	},
	{
		name:     "enroll",
		synopsis: "enroll [--recipients PATH]",
		help: "Unwraps the data key with an enrolled key in ssh-agent and wraps it for every\n" +
			"ed25519 key in ssh-agent that is not yet enrolled.",
		run: runEnroll,
	},
	{
		name:     "list",
		synopsis: "list [--recipients PATH]",
		help:     "Lists enrolled recipients and whether each is present in ssh-agent.",
		run:      runList,
	},
	{
		name:     "revoke",
		synopsis: "revoke [--recipients PATH] FINGERPRINT",
		help: "Removes the recipient with FINGERPRINT (as printed by `list`); the last recipient cannot be removed.\n" +
			"Revocation does not re-key: the revoked key can still unwrap the current data key from an earlier\n" +
			"copy of the recipients file. To protect state history, rotate the data key through OpenTofu's\n" +
			"`fallback` block.",
		run:   runRevoke,
		nargs: 1,
	},
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usage(stderr, exitUsage)
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return usage(stderr, exitOK)
	}
	cmd, ok := findCommand(args[0])
	if !ok {
		return report(stderr, exitUsage, fmt.Sprintf("unknown command %q\n\n%s", args[0], overview()))
	}

	flags := flag.NewFlagSet(programName+" "+cmd.name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {}
	recipients := flags.String("recipients", defaultRecipientsPath, "path to the recipients file")
	if err := flags.Parse(args[1:]); err != nil {
		code := exitUsage
		if errors.Is(err, flag.ErrHelp) {
			code = exitOK
		}
		return commandUsage(flags, &cmd, code)
	}
	if flags.NArg() != cmd.nargs {
		return commandUsage(flags, &cmd, exitUsage)
	}

	inv := &invocation{stdin: stdin, stdout: stdout, stderr: stderr, recipients: *recipients, args: flags.Args()}
	if err := cmd.run(ctx, inv); err != nil {
		return report(stderr, exitFailure, fmt.Sprintf("%s %s: %v\n", programName, cmd.name, err))
	}
	return exitOK
}

func findCommand(name string) (command, bool) {
	for i := range commands {
		if commands[i].name == name {
			return commands[i], true
		}
	}
	return command{}, false
}

func overview() string {
	synopses := make([]string, 0, len(commands))
	for i := range commands {
		synopses = append(synopses, "  "+programName+" "+commands[i].synopsis)
	}
	return "Usage:\n" + strings.Join(synopses, "\n") + "\n\nRun '" + programName + " <command> -h' for details.\n"
}

func usage(stderr io.Writer, code int) int {
	return report(stderr, code, overview())
}

func commandUsage(flags *flag.FlagSet, cmd *command, code int) int {
	text := fmt.Sprintf("Usage: %s %s\n\n%s\n\nFlags:\n", programName, cmd.synopsis, cmd.help)
	if _, err := io.WriteString(flags.Output(), text); err != nil {
		return exitFailure
	}
	flags.PrintDefaults()
	return code
}

func report(w io.Writer, code int, text string) int {
	if _, err := io.WriteString(w, text); err != nil {
		return exitFailure
	}
	return code
}
