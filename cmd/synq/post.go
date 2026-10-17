package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/term"

	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/postcli"
)

// runPost implements `synq post`: read stdin, sanitize it, unlock the
// identity (asking on the terminal, since stdin is the pipe), log in and
// create a Feed post. It never starts the TUI.
func runPost(args []string) error {
	opts, err := postcli.ParseArgs(args)
	if err != nil {
		return err
	}

	if term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("nothing piped in - use it like:  cat error.log | synq post")
	}
	raw, err := postcli.ReadInput(os.Stdin)
	if err != nil {
		return err
	}
	title, content, err := postcli.Build(raw, opts.Title)
	if err != nil {
		return err
	}

	// Everything above needed no key store; fail on bad input before
	// asking for a passphrase.
	tty, err := openTerminal()
	if err != nil {
		return fmt.Errorf("a terminal is needed to ask for your passphrase: %w", err)
	}
	defer tty.Close()
	passphraseInput = tty

	dbPath, err := keyStorePath()
	if err != nil {
		return fmt.Errorf("locate key store: %w", err)
	}
	ks, err := db.OpenKeyStore(dbPath)
	if err != nil {
		return fmt.Errorf("open key store: %w", err)
	}
	defer ks.Close()

	has, err := ks.HasIdentity()
	if err != nil {
		return fmt.Errorf("check for existing identity: %w", err)
	}
	if !has {
		return errors.New("no identity on this device yet - run synq once to create one")
	}
	apiClient, err := newAPIClient(ks)
	if err != nil {
		return err
	}
	if apiClient == nil {
		return errors.New("no server configured - run synq once to choose one")
	}

	id, refreshToken, passphrase, err := unlockIdentity(ks)
	if err != nil {
		return err
	}
	defer zeroBytes(passphrase)

	session := establishSession(ks, apiClient, id, passphrase, refreshToken)
	if session.Username == "" {
		return errors.New("this identity has no username yet - run synq once to register")
	}
	if session.AccessToken == "" {
		return errors.New("could not log in to the server (offline, or the account is gone)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	postID, err := session.API.CreatePost(ctx, session.AccessToken, opts.Category, title, content)
	if err != nil {
		return fmt.Errorf("server refused the post: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Posted to the Feed as %s [%s]: %s\n", session.Username, opts.Category, title)
	fmt.Println(postID)
	return nil
}

// openTerminal opens the controlling terminal for reading, independent
// of stdin.
func openTerminal() (*os.File, error) {
	name := "/dev/tty"
	if runtime.GOOS == "windows" {
		name = "CONIN$"
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}
