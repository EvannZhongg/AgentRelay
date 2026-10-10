package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"AgentRelay/internal/bridge"
	"AgentRelay/internal/protocol"
)

type environmentFlags map[string]string

func (e *environmentFlags) String() string { data, _ := json.Marshal(e); return string(data) }
func (e *environmentFlags) Set(value string) error {
	key, text, ok := strings.Cut(value, "=")
	if !ok || key == "" {
		return errors.New("--env requires KEY=VALUE")
	}
	if *e == nil {
		*e = environmentFlags{}
	}
	(*e)[key] = text
	return nil
}

func sessionCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay session create|list|get|send|close")
	}
	f := flag.NewFlagSet("session", flag.ContinueOnError)
	base := f.String("bridge", bridge.DefaultURL, "bridge URL")
	id := f.String("device", "", "device ID")
	sessionID := f.String("id", "", "session ID")
	dir := f.String("dir", "", "workspace-relative directory for create")
	input := f.String("input", "", "exact stdin text for send, including newlines")
	inputFile := f.String("input-file", "", "read input from a file, or - for stdin")
	var env environmentFlags
	f.Var(&env, "env", "environment override KEY=VALUE (repeatable)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("--device is required")
	}
	c, ctx := bridge.NewClient(*base), context.Background()
	switch args[0] {
	case "create":
		session, err := c.CreateSession(ctx, *id, protocol.SessionRequest{Command: strings.Join(f.Args(), " "), WorkingDir: *dir, Env: env})
		if err != nil {
			return err
		}
		return printJSON(session)
	case "list":
		sessions, err := c.ListSessions(ctx, *id)
		if err != nil {
			return err
		}
		return printJSON(sessions)
	case "get", "send", "close":
		if *sessionID == "" {
			return fmt.Errorf("session %s requires --id", args[0])
		}
		var session protocol.Session
		var err error
		switch args[0] {
		case "get":
			session, err = c.GetSession(ctx, *id, *sessionID)
		case "close":
			session, err = c.CloseSession(ctx, *id, *sessionID)
		case "send":
			provided := false
			f.Visit(func(v *flag.Flag) {
				if v.Name == "input" {
					provided = true
				}
			})
			if provided && *inputFile != "" {
				return errors.New("choose --input or --input-file")
			}
			if !provided && *inputFile == "" {
				return errors.New("session send requires --input or --input-file")
			}
			if *inputFile != "" {
				var data []byte
				if *inputFile == "-" {
					data, err = io.ReadAll(os.Stdin)
				} else {
					data, err = os.ReadFile(*inputFile)
				}
				if err != nil {
					return err
				}
				*input = string(data)
			}
			session, err = c.WriteSession(ctx, *id, *sessionID, *input)
		}
		if err != nil {
			return err
		}
		return printJSON(session)
	default:
		return fmt.Errorf("unknown session command %q", args[0])
	}
}
