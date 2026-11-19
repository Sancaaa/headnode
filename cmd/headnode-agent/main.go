package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"headnode/agent"
)

const usage = `usage:
  headnode-agent enroll -server URL -token TOKEN [-config agent.json]
  headnode-agent run [-config agent.json]`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "enroll":
		enrollCommand(os.Args[2:])
	case "run":
		runCommand(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func enrollCommand(args []string) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	server := fs.String("server", "", "server URL, for example http://127.0.0.1:8080")
	token := fs.String("token", "", "enroll token from `headnode token create`")
	config := fs.String("config", "agent.json", "where to save the credentials")
	fs.Parse(args)

	if *server == "" || *token == "" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := agent.Enroll(*server, *token, *config); err != nil {
		log.Fatal(err)
	}
	fmt.Println("enrolled, credentials saved to", *config)
}

func runCommand(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	config := fs.String("config", "agent.json", "credentials file written by enroll")
	fs.Parse(args)

	cfg, err := agent.LoadConfig(*config)
	if err != nil {
		log.Fatal(err)
	}

	// Ctrl+C (or `kill`) cancels ctx, which makes Run return cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = agent.Run(ctx, cfg)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
