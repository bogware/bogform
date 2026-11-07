// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Command bogform-server is the optional self-hosted Bogform dashboard. It runs on AWS Lambda or as an
// ordinary HTTP server, and is configured with BOG_SERVER_* environment variables.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bogware/bogform/internal/server"
)

const usage = `Usage: bogform-server <command>

Commands:
  serve                                    Run the HTTP server. On AWS Lambda, this is what happens by default.
  migrate                                  Update the database schema.
  create-token -name NAME -repository GLOB Create a token for pipelines to send reports with.
                                           For example, -repository 'acme/*' covers repositories under acme.
  list-tokens                              List tokens.
  revoke-token -name NAME                  Stop a token from working.
  prune                                    Delete runs older than the retention period.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := "serve"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	switch command {
	case "serve":
		return serve(ctx, args)
	case "migrate":
		return withDatabase(ctx, func(db *sql.DB) error { return server.Migrate(ctx, db) })
	case "create-token":
		flags := flag.NewFlagSet(command, flag.ExitOnError)
		name := flags.String("name", "", "A name for the token.")
		repository := flags.String("repository", "", `The repositories it can report for, such as "acme/infra" or "acme/*".`)
		_ = flags.Parse(args)
		return withDatabase(ctx, func(db *sql.DB) error {
			token, err := server.CreateToken(ctx, db, *name, *repository)
			if err != nil {
				return err
			}
			fmt.Printf("%s\n\nSave this as the BOG_SERVER_TOKEN secret in your pipeline. It can't be shown again.\n", token)
			return nil
		})
	case "list-tokens":
		return withDatabase(ctx, func(db *sql.DB) error {
			tokens, err := server.ListTokens(ctx, db)
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(tokens)
		})
	case "revoke-token":
		flags := flag.NewFlagSet(command, flag.ExitOnError)
		name := flags.String("name", "", "The token's name.")
		_ = flags.Parse(args)
		return withDatabase(ctx, func(db *sql.DB) error { return server.RevokeToken(ctx, db, *name) })
	case "prune":
		return withDatabase(ctx, func(db *sql.DB) error {
			cfg, err := server.LoadConfig(os.Getenv)
			if err != nil {
				return err
			}
			count, err := server.Prune(ctx, db, cfg.RetentionDays)
			if err == nil {
				fmt.Printf("Deleted %d runs.\n", count)
			}
			return err
		})
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", command, usage)
}

// environment returns environment variables, with secrets from AWS SSM Parameter Store if configured.
func environment(ctx context.Context) (func(string) string, error) {
	prefix := os.Getenv("BOG_SERVER_SSM_PREFIX")
	if prefix == "" {
		return os.Getenv, nil
	}
	return ssmEnvironment(ctx, prefix)
}

func withDatabase(ctx context.Context, action func(*sql.DB) error) error {
	env, err := environment(ctx)
	if err != nil {
		return err
	}
	// Admin commands only need the database, not the complete server configuration.
	url := env("BOG_SERVER_DATABASE_URL")
	if url == "" {
		return errors.New("BOG_SERVER_DATABASE_URL isn't set")
	}
	db, err := server.Open(url)
	if err != nil {
		return err
	}
	defer db.Close()
	return action(db)
}

func serve(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	address := flags.String("address", "", "The address to listen on. Defaults to :8080, or to 127.0.0.1:8080 if BOG_SERVER_DEV_USER is set.")
	_ = flags.Parse(args)

	env, err := environment(ctx)
	if err != nil {
		return err
	}
	cfg, err := server.LoadConfig(env)
	if err != nil {
		return err
	}
	if *address == "" {
		*address = ":8080"
		// This mode signs everyone in, so it must not listen on the network unless explicitly told to,
		// as the local Docker Compose setup does inside its container.
		if cfg.DevUser != "" {
			*address = "127.0.0.1:8080"
		}
	}
	db, err := server.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if cfg.AutoMigrate {
		migrateCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := server.Migrate(migrateCtx, db); err != nil {
			return fmt.Errorf("updating the database: %w", err)
		}
	}
	srv := server.New(cfg, db)
	handler := srv.Handler()

	if runtimeAPI := os.Getenv("AWS_LAMBDA_RUNTIME_API"); runtimeAPI != "" {
		return server.RunLambda(ctx, runtimeAPI, handler, srv.Wait)
	}

	httpServer := &http.Server{
		Addr:              *address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "Listening on %s, serving %s\n", *address, strings.TrimSpace(cfg.PublicURL))
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Let alerts that are still being sent finish.
	srv.Wait()
	return nil
}
