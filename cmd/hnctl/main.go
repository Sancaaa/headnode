// hnctl is the command-line client of headnode: it logs in to the server and
// forwards ports on your own computer to ports on hosts, through the server,
// even when those hosts sit behind NAT (architecture.md §2).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"golang.org/x/term"
)

const usage = `usage:
  hnctl login -server URL [-username NAME]      log in (the password is asked for)
  hnctl logout                                  end the login
  hnctl hosts                                   list the hosts you can see
  hnctl forward [-bind ADDR] PORT:HOST:TARGET   forward a local port to a port on a host

example:
  hnctl forward 2222:web-01:127.0.0.1:22
  ssh -p 2222 root@localhost

HOST is a host's name or id (see "hnctl hosts"). TARGET is host:port on that
host, and can only be the host itself: 127.0.0.1 or localhost.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "login":
		loginCommand(args)
	case "logout":
		logoutCommand()
	case "hosts":
		hostsCommand()
	case "forward":
		forwardCommand(args)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "hnctl: "+format+"\n", args...)
	os.Exit(1)
}

// ------------------------------------------------------------------ login

func loginCommand(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	serverFlag := fs.String("server", "", "server address, for example https://headnode.example.com")
	username := fs.String("username", "", "your username (asked for if left out)")
	fs.Parse(args)

	// If no server is given, the one from the last login is used again.
	serverText := *serverFlag
	if serverText == "" {
		if old, err := loadConfig(); err == nil {
			serverText = old.Server
		}
	}
	if serverText == "" {
		fatalf("give the server address: hnctl login -server https://headnode.example.com")
	}
	server, err := normalizeServer(serverText)
	if err != nil {
		fatalf("%v", err)
	}

	in := bufio.NewReader(os.Stdin)
	if *username == "" {
		fmt.Fprint(os.Stderr, "Username: ")
		line, err := readLine(in)
		if err != nil {
			fatalf("read username: %v", err)
		}
		*username = line
	}
	password, err := readPassword(in)
	if err != nil {
		fatalf("%v", err)
	}

	cfg, err := login(server, strings.TrimSpace(*username), password)
	if err != nil {
		fatalf("login failed: %v", err)
	}
	if err := saveConfig(cfg); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("logged in to %s as %s\n", cfg.Server, cfg.Username)
}

// readPassword asks for the password without showing it. When input is piped (a
// script) it reads one plain line instead.
func readPassword(in *bufio.Reader) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return readLine(in)
	}

	// ReadPassword turns echo off and restores it when it returns. If the user
	// presses Ctrl+C in between, the terminal would be left with echo off, so
	// restore it first.
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
		os.Exit(130)
	}()

	fmt.Fprint(os.Stderr, "Password: ")
	password, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr) // Enter is not echoed either
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(password), nil
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ----------------------------------------------------------------- logout

func logoutCommand() {
	cfg, err := loadConfig()
	if err != nil {
		fatalf("%v", err)
	}
	// End the session on the server too, so the token stops working even if a
	// copy of it exists somewhere. If the server cannot be reached, the local
	// login is still removed.
	if err := newClient(cfg).do("POST", "/api/logout", nil, nil); err != nil {
		logf("the server could not end the session (%v), removing the local login anyway", err)
	}
	if err := deleteConfig(); err != nil {
		fatalf("%v", err)
	}
	fmt.Println("logged out")
}

// ------------------------------------------------------------------ hosts

func hostsCommand() {
	cfg, err := loadConfig()
	if err != nil {
		fatalf("%v", err)
	}
	var hosts []hostInfo
	if err := newClient(cfg).do("GET", "/api/hosts", nil, &hosts); err != nil {
		fatalf("%v", err)
	}

	// Aligned columns on standard output, so it reads well and still works with grep.
	table := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tID\tSTATUS\tGROUP\tLAN\tWAN")
	for _, h := range hosts {
		status := "offline"
		if h.Online {
			status = "online"
		}
		group := "-"
		if h.GroupName != nil {
			group = *h.GroupName
		}
		lan := "-"
		if h.Info != nil && len(h.Info.LANIPs) > 0 {
			lan = strings.Join(h.Info.LANIPs, ",")
		}
		wan := h.WANAddr
		if wan == "" {
			wan = "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", h.Name, h.ID, status, group, lan, wan)
	}
	table.Flush()
}

// ---------------------------------------------------------------- forward

func forwardCommand(args []string) {
	fs := flag.NewFlagSet("forward", flag.ExitOnError)
	bind := fs.String("bind", "127.0.0.1", "address to listen on")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	spec, err := parseForward(fs.Arg(0))
	if err != nil {
		fatalf("%v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		fatalf("%v", err)
	}
	c := newClient(cfg)

	var hosts []hostInfo
	if err := c.do("GET", "/api/hosts", nil, &hosts); err != nil {
		fatalf("%v", err)
	}
	host, err := resolveHost(hosts, spec.host)
	if err != nil {
		fatalf("%v", err)
	}
	if !host.CanShell {
		fatalf("you may not open tunnels to %s: that needs the operator role in its group", host.Name)
	}
	if !host.Online {
		logf("%s is offline: connections will fail until it is back", host.Name)
	}

	// Loopback by default: the local end has no password, so whoever can reach
	// the port can use the tunnel, with your login.
	if ip := net.ParseIP(*bind); ip == nil || !ip.IsLoopback() {
		logf("warning: %s is reachable from other machines, and anyone who connects uses the tunnel as you", *bind)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(*bind, strconv.Itoa(spec.localPort)))
	if err != nil {
		fatalf("cannot listen on port %d: %v", spec.localPort, err)
	}
	logf("forwarding %s to %s (%s) %s. Press Ctrl+C to stop.", ln.Addr(), host.Name, host.ID, spec.target)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ln.Close() // makes Accept fail, which ends serveForward
	}()
	serveForward(ctx, ln, c, host, spec.target)
}
