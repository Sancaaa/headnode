package server

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

const (
	// A terminal burst or a file chunk fits easily. The library default (32 KB)
	// is a little tight for a PTY that can read more than that at once.
	relayReadLimit = 1 << 20

	// A session can sit idle for hours. A ping every 30 s keeps routers and
	// proxies from dropping the connection, and it is also how we notice that
	// a peer is gone without having said goodbye (a laptop that went to sleep).
	relayPingEvery   = 30 * time.Second
	relayPingTimeout = 10 * time.Second

	// protocol.md §5: the other side of the session did not arrive in time.
	closeNoPeer websocket.StatusCode = 4000
)

// relay joins two WebSocket connections and copies messages between them until
// either side ends, then closes both (architecture.md §3.2).
//
// It does not look inside the messages. Terminal bytes, resize commands and
// later the file manager all pass through unchanged: text stays text and
// binary stays binary. That is what keeps this function the same for every
// kind of session.
func relay(ctx context.Context, a, b *websocket.Conn) {
	a.SetReadLimit(relayReadLimit)
	b.SetReadLimit(relayReadLimit)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// What stopped the relay first: the connection that broke, the one still
	// alive, and why. Three goroutines can report, each at most once.
	type ending struct {
		broken, alive *websocket.Conn
		err           error
	}
	ended := make(chan ending, 3)

	var wg sync.WaitGroup
	pump := func(from, to *websocket.Conn) {
		defer wg.Done()
		for {
			kind, data, err := from.Read(ctx)
			if err != nil {
				ended <- ending{broken: from, alive: to, err: err}
				return
			}
			if err := to.Write(ctx, kind, data); err != nil {
				ended <- ending{broken: to, alive: from, err: err}
				return
			}
		}
	}
	keepAlive := func() {
		defer wg.Done()
		ticker := time.NewTicker(relayPingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			for _, pair := range [][2]*websocket.Conn{{a, b}, {b, a}} {
				pingCtx, cancelPing := context.WithTimeout(ctx, relayPingTimeout)
				err := pair[0].Ping(pingCtx)
				cancelPing()
				if err != nil {
					if ctx.Err() == nil { // not just us shutting down
						ended <- ending{broken: pair[0], alive: pair[1], err: err}
					}
					return
				}
			}
		}
	}

	wg.Add(3)
	go pump(a, b)
	go pump(b, a)
	go keepAlive()

	first := <-ended

	// Tell the side that is still alive what happened (a normal close, or the
	// agent's error text), and only then stop the goroutines. Cancelling first
	// would make the library drop the connection without saying anything.
	// Close waits for the peer's answer, which the other pump is reading.
	code, reason := closeStatus(first.err)
	first.alive.Close(code, reason)
	first.broken.CloseNow()
	cancel()
	wg.Wait()
}

// closeStatus turns what ended a connection into the close code and reason to
// pass on to the other side. When one side closed on purpose, the other side
// gets the same code and reason: that is how "the shell exited" or "cannot
// start a shell: ..." from the agent reaches the browser.
func closeStatus(err error) (websocket.StatusCode, string) {
	var closed websocket.CloseError
	if errors.As(err, &closed) {
		switch closed.Code {
		case websocket.StatusNoStatusRcvd:
			return websocket.StatusNormalClosure, "" // "closed, no reason given"
		case websocket.StatusAbnormalClosure, websocket.StatusTLSHandshake:
			// These codes are only ever reported locally and may not be sent.
		default:
			return closed.Code, shortReason(closed.Reason)
		}
	}
	return websocket.StatusGoingAway, "the other side disconnected"
}

// shortReason fits a close reason into the 123 bytes a close frame allows,
// without cutting a character in half.
func shortReason(s string) string {
	for len(s) > 120 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
