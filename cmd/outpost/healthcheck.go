package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// healthcheckCmd asks the daemon's own /health whether it is serving.
//
// This is the container's HEALTHCHECK, and it is the binary asking itself on
// purpose: a distroless image has no curl and no shell, and "the process is
// running" is not the question that matters — a daemon whose listener is dead but
// whose loop is alive would report healthy for ever. The update watcher reads the
// result to decide whether to keep a new image or put the old one back.
//
// It reads config.yaml for the listen address (cfg.Listen may be 0.0.0.0, so the
// probe always dials loopback) and takes --addr for the case where the answer is
// not the daemon's own listener.
func (a *app) healthcheckCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Ask the daemon whether it is serving (exit 1 if not)",
		RunE: func(cmd *cobra.Command, args []string) error {
			url := addr
			if url == "" {
				url = healthURL(a.cfg.Listen)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("%s: %w", url, err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s answered %s: %.200s", url, resp.Status, strings.TrimSpace(string(body)))
			}
			fmt.Fprint(os.Stdout, string(body))
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "URL to probe instead of the configured listener")
	return cmd
}

// healthURL turns a listen address into the loopback URL of /health. An empty
// listen (a config that never set it) falls back to the documented default rather
// than to a zero port.
func healthURL(listen string) string {
	if listen == "" {
		listen = "127.0.0.1:8896"
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		// A bare port or something unparseable: let the probe fail loudly with the
		// address in the message rather than guessing silently.
		port = listen
	}
	return "http://127.0.0.1:" + port + "/health"
}
