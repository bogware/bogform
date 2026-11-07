// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/dashboard"
	"github.com/bogware/bogform/internal/webui"
)

// UICommander builds the "ui" command, which shows pipeline reports in a dashboard on this computer.
func UICommander() Command {
	cmd := Command{
		Name:  "ui",
		Short: "Show pipeline reports in a local dashboard",
		Long: `Shows the reports that "bogform ci" writes, in the same dashboard that you can host yourself, without hosting anything. It searches the directories you name, or the current repository, for .bogform-ci/report.json files.

The dashboard is read-only, and only answers on this computer. Its address includes a random token, so other people and programs on this computer can't read it.`,
	}
	var dirs []string
	var address string
	var noBrowser bool
	cmd.CommandLine.VariadicArg(&dirs, "dir")
	cmd.CommandLine.StringVar(&address, "address", "127.0.0.1:0", "The local address to listen on. Port 0 picks a free port.")
	cmd.CommandLine.BoolVar(&noBrowser, "no-browser", false, "Don't open the dashboard in a web browser.")

	cmd.Run = func(meta Meta) int {
		ctx, done := meta.InterruptibleContext(meta.CommandContext())
		defer done()
		if len(dirs) == 0 {
			dirs = []string{defaultUIRoot()}
		}
		var launch func(string) error
		if !noBrowser && meta.BrowserLauncher != nil {
			launch = meta.BrowserLauncher.OpenURL
		}
		if err := runUI(ctx, dirs, address, launch, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return 1
		}
		return 0
	}
	return cmd
}

// defaultUIRoot is the current repository's top directory, or the current directory outside one.
func defaultUIRoot() string {
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return "."
}

func runUI(ctx context.Context, dirs []string, address string, launch func(string) error, out io.Writer) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%q isn't a valid address, such as 127.0.0.1:8080", address)
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("the local dashboard only listens on this computer. Use an address such as 127.0.0.1:8080")
	}
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return fmt.Errorf("%s isn't a directory", dir)
		}
	}

	reports, problems := dashboard.FindReports(dirs)
	for _, problem := range problems {
		fmt.Fprintf(out, "Warning: %s\n", problem)
	}
	fmt.Fprintf(out, "Found %d %s in %s.\n", len(reports), plural(len(reports), "report", "reports"), strings.Join(dirs, ", "))

	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("couldn't listen on %s: %w", address, err)
	}
	assets := webui.FS()
	server := &http.Server{
		Handler:           dashboard.NewLocal(dirs, token, assets).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := fmt.Sprintf("http://%s/?token=%s", listener.Addr().String(), token)
	if assets == nil {
		fmt.Fprintln(out, "This build doesn't include the dashboard's web page, so only its data is available. Release builds include it.")
	}
	fmt.Fprintf(out, "\nThe dashboard is at:\n\n  %s\n\nIt updates as reports change. Press Ctrl+C to stop.\n", url)
	if launch != nil {
		if err := launch(url); err != nil {
			fmt.Fprintf(out, "Couldn't open a browser: %s\n", err)
		}
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
