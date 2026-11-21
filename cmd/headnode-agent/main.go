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
  headnode-agent run [-config agent.json]
  headnode-agent service install [-config FILE] [-user NAME]
                                                  start at boot: a systemd service on Linux (needs root),
                                                  a Windows service on Windows (needs Administrator).
                                                  -user: the Linux account the agent runs as (terminals and
                                                  files are that user's); without it, root
  headnode-agent service uninstall                remove that service
  headnode-agent service allow -sid SID           (Windows, Administrator) let that user switch the service and
                                                  TightVNC's on and off without administrator rights
  headnode-agent tray                             (Windows) a tray icon: connected or not, Turn headnode off / on,
                                                  Start / Stop VNC

The service's default credentials file is /etc/headnode/agent.json on Linux and
%ProgramData%\headnode\agent.json on Windows.`

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
	case "service":
		serviceCommand(os.Args[2:])
	case "tray":
		if err := agent.RunTray(); err != nil {
			log.Fatal(err)
		}
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

// serviceCommand installs or removes the systemd service (M6). The credentials
// file has an absolute default path here, unlike the other commands: a service
// does not start in the folder the person was in.
func serviceCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("service "+args[0], flag.ExitOnError)
	config := fs.String("config", agent.DefaultConfigPath(), "credentials file written by enroll")
	runUser := fs.String("user", "", "(install, Linux) the account the agent runs as: terminals and files are that user's. Default: root")
	sid := fs.String("sid", "", "(allow, Windows) the security identifier of the user who may switch the service")
	fs.Parse(args[1:])

	switch args[0] {
	case "allow":
		if *sid == "" {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		if err := agent.AllowService(*sid); err != nil {
			log.Fatal(err)
		}
		fmt.Println("that user may now turn headnode (and TightVNC) on and off without administrator rights.")
	case "install":
		if err := agent.InstallService(*config, *runUser); err != nil {
			log.Fatal(err)
		}
		fmt.Println("headnode-agent is installed as a service and running.", agent.LogHint())
	case "uninstall":
		if err := agent.UninstallService(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("the headnode-agent service is removed. The program and", *config, "were left in place.")
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func runCommand(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	config := fs.String("config", "agent.json", "credentials file written by enroll")
	fs.Parse(args)

	cfg, err := agent.LoadConfig(*config)
	if err != nil {
		log.Fatal(err)
	}

	// Started by the Windows service manager: it has its own way to start and stop
	// a program, and no console to log to (agent.RunService deals with both).
	if agent.RunningAsService() {
		if err := agent.RunService(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Ctrl+C (or `kill`) cancels ctx, which makes Run return cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = agent.Run(ctx, cfg)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
