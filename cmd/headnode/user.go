package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/term"

	"headnode/server"
)

// userCommand handles `headnode user create` and `headnode user totp-reset`.
// Like `token create`, it talks to the database directly: the very first user
// and emergency 2FA recovery have to work without a dashboard login.
func userCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch args[0] {
	case "create":
		userCreateCommand(args[1:])
	case "totp-reset":
		userTOTPResetCommand(args[1:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func userCreateCommand(args []string) {
	fs := flag.NewFlagSet("user create", flag.ExitOnError)
	username := fs.String("username", "", "name to log in with")
	admin := fs.Bool("admin", false, "make this user an administrator")
	dbPath := fs.String("db", "data/headnode.db", "path of the SQLite database")
	fs.Parse(args)

	if *username == "" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	// The password is never a flag: flags show up in the process list and in
	// the shell history.
	password, err := readPassword()
	if err != nil {
		log.Fatal(err)
	}

	db := openDB(*dbPath)
	defer db.Close()

	if err := server.CreateUser(db, *username, password, *admin); err != nil {
		log.Fatal(err)
	}
	fmt.Println("user created")
}

func userTOTPResetCommand(args []string) {
	fs := flag.NewFlagSet("user totp-reset", flag.ExitOnError)
	username := fs.String("username", "", "user whose authenticator should be reset")
	dbPath := fs.String("db", "data/headnode.db", "path of the SQLite database")
	fs.Parse(args)

	if *username == "" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	db := openDB(*dbPath)
	defer db.Close()

	if err := server.ResetUserTOTP(db, *username); err != nil {
		log.Fatal(err)
	}
	fmt.Println("authenticator reset; user will set up a new one on next login")
}

// readPassword gets the password without ever showing it.
//
// On a terminal it prompts twice with echo turned off. When input is piped
// (a script), it reads one line instead, for example in PowerShell:
//
//	"my password" | go run ./cmd/headnode user create -username admin -admin
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return readLine(bufio.NewReader(os.Stdin))
	}

	// ReadPassword turns echo off and restores it when it returns. If the user
	// presses Ctrl+C in between, the process would die with echo still off and
	// the terminal would look broken. So catch Ctrl+C, restore, then exit.
	oldState, err := term.GetState(fd)
	if err != nil {
		return "", fmt.Errorf("read terminal state: %w", err)
	}
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	defer signal.Stop(interrupted)
	go func() {
		<-interrupted
		term.Restore(fd, oldState)
		fmt.Fprintln(os.Stderr)
		os.Exit(130) // the usual exit code for "stopped by Ctrl+C"
	}()

	first, err := askPassword(fd, "Password: ")
	if err != nil {
		return "", err
	}
	second, err := askPassword(fd, "Repeat password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("passwords do not match")
	}
	return first, nil
}

func askPassword(fd int, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	password, err := term.ReadPassword(fd)
	// Enter is not echoed either, so the cursor would stay on the prompt line.
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(password), nil
}

// readLine returns one line without its line ending. Spaces are kept: they
// can be part of a password.
func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
