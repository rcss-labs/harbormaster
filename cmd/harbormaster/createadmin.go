package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const createAdminUsage = "usage: harbormaster create-admin --username <name> [--password-stdin]"

// errUserExists maps to exit status 3.
var errUserExists = errors.New("user already exists")

// runCreateAdmin creates a break-glass administrator who must change their password at first sign-in.
// Break-glass keeps a password sign-in working while SSO is enabled.
func runCreateAdmin(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	username := flags.String("username", "", "username of the new administrator")
	fromStdin := flags.Bool("password-stdin", false, "read the password from standard input")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 || *username == "" {
		return errUsage{createAdminUsage}
	}
	if err := auth.ValidateUsername(*username); err != nil {
		return err
	}

	password, generated := "", false
	if *fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read password: %w", err)
		}
		password = strings.TrimRight(line, "\r\n")
		if err := auth.ValidatePassword(password); err != nil {
			return err
		}
	} else {
		password, generated = auth.GenerateTemporaryPassword(), true
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	dbURL, err := resolveDatabaseURL(ctx, cfg)
	if err != nil {
		return err
	}
	st, err := openStore(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if _, err := st.GetUserByUsername(ctx, *username); err == nil {
		return fmt.Errorf("%w: %s", errUserExists, *username)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := st.CreateBreakGlassAdmin(ctx, *username, hash); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("%w: %s", errUserExists, *username)
		}
		return err
	}
	fmt.Fprintf(stdout, "created break-glass admin %s (must change password at first sign-in)\n", *username)
	if generated {
		fmt.Fprintf(stdout, "temporary password: %s\n", password)
	}
	return nil
}
