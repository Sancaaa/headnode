package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"headnode/server"
)

const usage = `usage:
  headnode [-addr host:port] [-db path] [-agents dir]         run the server
  headnode token create [-db path]                            create an agent enroll token
  headnode user create -username NAME [-admin] [-db path]     create a dashboard user
                                                              (asks for the password, or reads it from stdin when piped)
  headnode user totp-reset -username NAME [-db path]          reset a user's authenticator and end their sessions`

func main() {
	// "token create" and "user create" are CLI commands on the server machine.
	// They talk to the database directly, so no HTTP endpoint exists that
	// creates tokens or users without a login (protocol.md §1).
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "token":
			tokenCommand(os.Args[2:])
			return
		case "user":
			userCommand(os.Args[2:])
			return
		}
	}
	serve(os.Args[1:])
}

func serve(args []string) {
	fs := flag.NewFlagSet("headnode", flag.ExitOnError)
	// 127.0.0.1 by default: the server speaks plain HTTP and Caddy, on the same
	// machine, adds TLS (D-07). Passwords and session cookies must not travel
	// over plain HTTP, so only listen on other addresses on a network you trust.
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	dbPath := fs.String("db", "data/headnode.db", "path of the SQLite database")
	agentDir := fs.String("agents", "dist", "folder with the built agent programs (scripts/build-agents), served at /download")
	fs.Parse(args)

	db := openDB(*dbPath)
	defer db.Close()

	srv := server.New(db)
	srv.UseAgentDir(*agentDir)
	// Uploaded files live next to the database: data/headnode.db, data/files/.
	if err := srv.UseFilesDir(filepath.Join(filepath.Dir(*dbPath), "files")); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{
		Addr:    *addr,
		Handler: srv.Handler(),
		// Without this, a client that opens a connection and never finishes its
		// request headers holds that connection open forever.
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("headnode listening on http://%s", *addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			db.Close()
			log.Fatal(err)
		}
	case <-ctx.Done():
		log.Printf("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			db.Close()
			log.Fatal(err)
		}
	}
}

func tokenCommand(args []string) {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("token create", flag.ExitOnError)
	dbPath := fs.String("db", "data/headnode.db", "path of the SQLite database")
	fs.Parse(args[1:])

	db := openDB(*dbPath)
	defer db.Close()

	token, expiresAt, err := server.CreateEnrollToken(db)
	if err != nil {
		log.Fatal(err)
	}
	// Printed to stdout and nothing else, so it can be captured:  TOKEN=$(headnode token create)
	// The explanation goes to stderr for the same reason.
	fmt.Fprintf(os.Stderr, "enroll token (valid until %s, shown only once):\n", expiresAt.Format("2006-01-02 15:04"))
	fmt.Println(token)
}

// openDB creates the data directory if needed, then opens the database.
//
// The directory holds password hashes, agent secret hashes and uploaded files,
// so only its owner may enter it (0700). MkdirAll never changes the mode of a
// directory that already exists: one made by an older version stays 0755 until
// someone runs chmod 700 on it (roadmap, M6 deploy checklist).
func openDB(path string) *sql.DB {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Fatal(err)
	}
	db, err := server.OpenDB(path)
	if err != nil {
		log.Fatal(err)
	}
	return db
}
