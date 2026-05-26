// Command wsclient is a tiny WebSocket client for end-to-end and manual
// testing of the notification status broadcast endpoint
// (/api/v1/ws/notifications). It connects, sends a subscribe frame for
// the supplied notification id, and prints every server message it
// receives for the duration (default 30s).
//
// Usage:
//
//	go run ./tests/e2e/wsclient <ws-url> <notification-id> [duration]
//
// Example:
//
//	go run ./tests/e2e/wsclient \
//	    ws://localhost:8080/api/v1/ws/notifications \
//	    019e5f7d-3ef0-7e11-b0b1-d0b25bcbca0f 30s
//
// Designed for E2E reporting: every received frame is printed as
// "[recv] <payload>" so a reader can diff the expected lifecycle
// transitions against actual server output.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "wsclient: %v\n", err)
		os.Exit(1)
	}
}

// run is the testable entry point. main wraps it so the only os.Exit
// in the program is at the top-level — keeps gosec G706 quiet (the
// linter flags any log.Fatalf that interpolates user-controlled input
// even when it is parameterized through %v / %q).
func run() error {
	url, notifID, dur, err := parseArgs(os.Args)
	if err != nil {
		return err
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := dialAndSubscribe(rootCtx, url, notifID)
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseNow() }()

	return readLoop(rootCtx, conn, dur)
}

// parseArgs validates the CLI args and returns the dial url, the
// notification id to subscribe to, and how long to keep reading. A
// missing duration defaults to 30s.
func parseArgs(argv []string) (string, string, time.Duration, error) {
	if len(argv) < 3 {
		return "", "", 0, fmt.Errorf("usage: %s <ws-url> <notification-id> [duration]", argv[0])
	}
	dur := 30 * time.Second
	if len(argv) >= 4 {
		d, err := time.ParseDuration(argv[3])
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid duration %q: %w", argv[3], err)
		}
		dur = d
	}
	return argv[1], argv[2], dur, nil
}

// dialAndSubscribe opens the websocket and sends the subscribe frame.
// Returns the live connection so the caller can drive the read loop
// and own the close defer.
func dialAndSubscribe(ctx context.Context, url, notifID string) (*websocket.Conn, error) {
	dialCtx, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	defer cancelDial()

	conn, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", url, err)
	}

	fmt.Printf("[connected] %s\n", url)

	subMsg, _ := json.Marshal(map[string]string{
		"action":          "subscribe",
		"notification_id": notifID,
	})
	if werr := conn.Write(ctx, websocket.MessageText, subMsg); werr != nil {
		_ = conn.CloseNow()
		return nil, fmt.Errorf("send subscribe: %w", werr)
	}
	fmt.Printf("[sent] %s\n", subMsg)
	return conn, nil
}

// readLoop pulls frames off the connection for the configured duration,
// dumping each payload to stdout. Deadline / cancellation / server
// close all surface as clean exits; unexpected read errors are
// returned.
func readLoop(ctx context.Context, conn *websocket.Conn, dur time.Duration) error {
	readCtx, cancelRead := context.WithTimeout(ctx, dur)
	defer cancelRead()

	for {
		_, payload, rerr := conn.Read(readCtx)
		if rerr != nil {
			if errors.Is(rerr, context.DeadlineExceeded) || errors.Is(rerr, context.Canceled) {
				fmt.Printf("[done] read window elapsed (%s)\n", dur)
				return nil
			}
			var closeErr websocket.CloseError
			if errors.As(rerr, &closeErr) {
				fmt.Printf("[closed] code=%d reason=%q\n", closeErr.Code, closeErr.Reason)
				return nil
			}
			return fmt.Errorf("read: %w", rerr)
		}
		fmt.Printf("[recv] %s\n", payload)
	}
}
