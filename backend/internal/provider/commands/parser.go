// Package commands parses slash commands out of inbound chat messages.
//
// The parser is provider-agnostic: it inspects only the message text. It
// does NOT execute commands — callers (the inbound relay) decide what to
// do with each parsed Command. Anything that does not start with "/" is
// flagged ErrNotACommand so the relay knows to forward it as regular
// chat to Moses Manager.
package commands

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

type Command struct {
	Verb string
	Args []string
}

var (
	ErrNotACommand    = errors.New("commands: not a slash command")
	ErrUnknownCommand = errors.New("commands: unknown slash command")
	ErrInvalidArgs    = errors.New("commands: invalid arguments for command")
)

var (
	linkCodeRE = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)
	tenantRE   = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	dndDayRE   = regexp.MustCompile(`^[1-7]d$`)
	verbRE     = regexp.MustCompile(`^/[a-z][a-z0-9_]*$`)
)

// argSpec validates the args of a recognized verb. nil means zero args.
type argSpec func(args []string) error

var commandSpecs = map[string]argSpec{
	"/start":     zeroArgs,
	"/unlink":    zeroArgs,
	"/help":      zeroArgs,
	"/tickets":   zeroArgs,
	"/status":    zeroArgs,
	"/clear":     zeroArgs,
	"/link":      linkArgs,
	"/use":       useArgs,
	"/autopilot": autopilotArgs,
	"/dnd":       dndArgs,
}

func Parse(text string) (Command, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return Command{}, ErrNotACommand
	}

	fields := strings.Fields(trimmed)
	verb := strings.ToLower(fields[0])
	if !verbRE.MatchString(verb) {
		return Command{}, ErrUnknownCommand
	}

	var args []string
	if len(fields) > 1 {
		args = fields[1:]
	}

	spec, ok := commandSpecs[verb]
	if !ok {
		return Command{}, ErrUnknownCommand
	}
	if err := spec(args); err != nil {
		// Return the recognised verb alongside the error so the relay can
		// still route a known-but-malformed command (e.g. "/autopilot wat")
		// to its handler — which replies with a usage hint — instead of
		// silently forwarding a "/command ..." message to Moses Manager.
		return Command{Verb: verb, Args: args}, err
	}
	return Command{Verb: verb, Args: args}, nil
}

func zeroArgs(args []string) error {
	if len(args) != 0 {
		return ErrInvalidArgs
	}
	return nil
}

func linkArgs(args []string) error {
	if len(args) != 1 || !linkCodeRE.MatchString(args[0]) {
		return ErrInvalidArgs
	}
	return nil
}

func useArgs(args []string) error {
	if len(args) != 1 || !tenantRE.MatchString(args[0]) {
		return ErrInvalidArgs
	}
	return nil
}

func autopilotArgs(args []string) error {
	if len(args) != 1 {
		return ErrInvalidArgs
	}
	// Case-insensitive: mobile keyboards autocapitalise, so a user typing
	// "/autopilot start" frequently sends "/autopilot Start".
	switch strings.ToLower(args[0]) {
	case "start", "stop", "status":
		return nil
	default:
		return ErrInvalidArgs
	}
}

// dndArgs accepts either a Go duration (e.g. "2h", "30s") or a 1-7 day
// shorthand ("1d".."7d"). The parser does not enforce a minimum useful
// duration — the bot layer decides whether 30s is too short.
func dndArgs(args []string) error {
	if len(args) != 1 {
		return ErrInvalidArgs
	}
	a := args[0]
	if dndDayRE.MatchString(a) {
		return nil
	}
	if _, err := time.ParseDuration(a); err != nil {
		return ErrInvalidArgs
	}
	return nil
}
