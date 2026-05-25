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
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: %s <ws-url> <notification-id> [duration]", os.Args[0])
	}
	url := os.Args[1]
	notifID := os.Args[2]

	dur := 30 * time.Second
	if len(os.Args) >= 4 {
		d, err := time.ParseDuration(os.Args[3])
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", os.Args[3], err)
		}
		dur = d
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dialCtx, cancelDial := context.WithTimeout(rootCtx, 10*time.Second)
	defer cancelDial()

	conn, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", url, err)
	}
	defer func() { _ = conn.CloseNow() }()

	fmt.Printf("[connected] %s\n", url)

	subMsg, _ := json.Marshal(map[string]string{
		"action":          "subscribe",
		"notification_id": notifID,
	})
	if err := conn.Write(rootCtx, websocket.MessageText, subMsg); err != nil {
		return fmt.Errorf("send subscribe: %w", err)
	}
	fmt.Printf("[sent] %s\n", subMsg)

	readCtx, cancelRead := context.WithTimeout(rootCtx, dur)
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
