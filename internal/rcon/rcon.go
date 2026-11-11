// Package rcon is a small Minecraft RCON client.
//
// It exists so the orchestrator can ask the server two questions — "are you up"
// and "who is playing" — and tell it one thing — "shut down cleanly". That single
// channel replaces v1's ready/heartbeat/state-change webhooks and the log-scraping
// watchdog, which is the whole point of the rewrite.
package rcon

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Packet types.
const (
	typeResponseValue = 0
	typeCommand       = 2
	typeAuth          = 3
)

// Client is a single RCON connection. It is safe for concurrent use.
type Client struct {
	conn net.Conn
	mu   sync.Mutex
	id   int32
}

// Dial connects and authenticates. A wrong password is reported as such rather
// than as a generic read error.
func Dial(ctx context.Context, addr, password string) (*Client, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial rcon %s: %w", addr, err)
	}

	c := &Client{conn: conn, id: 1}
	if err := c.write(typeAuth, password); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send auth: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	id, typ, _, err := c.read()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read auth response: %w", err)
	}
	if typ == typeAuth && id == -1 {
		conn.Close()
		return nil, fmt.Errorf("rcon: authentication failed (wrong password)")
	}

	// Some servers follow a successful auth with an empty response packet. Drain
	// anything already buffered so the first Command does not read stale bytes.
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	for {
		if _, _, _, err := c.read(); err != nil {
			break
		}
	}
	_ = conn.SetReadDeadline(time.Time{})

	return c, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) write(typ int32, payload string) error {
	body := []byte(payload)
	buf := make([]byte, 0, 14+len(body))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(4+4+len(body)+2))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(c.id))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(typ))
	buf = append(buf, body...)
	buf = append(buf, 0x00, 0x00)

	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(buf)
	return err
}

func (c *Client) read() (id int32, typ int32, payload string, err error) {
	var size int32
	if err := binary.Read(c.conn, binary.LittleEndian, &size); err != nil {
		return 0, 0, "", err
	}
	if size < 10 || size > 1<<20 {
		return 0, 0, "", fmt.Errorf("rcon: implausible packet length %d", size)
	}

	raw := make([]byte, size)
	if _, err := io.ReadFull(c.conn, raw); err != nil {
		return 0, 0, "", err
	}

	id = int32(binary.LittleEndian.Uint32(raw[0:4]))
	typ = int32(binary.LittleEndian.Uint32(raw[4:8]))
	payload = strings.TrimRight(string(raw[8:]), "\x00")
	return id, typ, payload, nil
}

// Command runs cmd and returns its output.
//
// Vanilla splits long replies across packets, so this keeps reading until the
// server stops talking for a moment. A short timeout after the first packet is
// cheaper than parsing the protocol's fragment terminator.
func (c *Client) Command(ctx context.Context, cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	c.id++
	want := c.id
	if err := c.write(typeCommand, cmd); err != nil {
		return "", fmt.Errorf("send %q: %w", cmd, err)
	}

	var out strings.Builder
	deadline := time.Now().Add(5 * time.Second)

	for {
		_ = c.conn.SetReadDeadline(deadline)
		id, typ, payload, err := c.read()
		if err != nil {
			if out.Len() > 0 && isTimeout(err) {
				break
			}
			return out.String(), fmt.Errorf("read %q: %w", cmd, err)
		}
		if typ != typeResponseValue || id != want {
			continue
		}
		out.WriteString(payload)
		deadline = time.Now().Add(300 * time.Millisecond)
	}

	return out.String(), nil
}

func isTimeout(err error) bool {
	var ne net.Error
	if ok := asNetError(err, &ne); ok && ne.Timeout() {
		return true
	}
	return false
}

func asNetError(err error, target *net.Error) bool {
	if ne, ok := err.(net.Error); ok {
		*target = ne
		return true
	}
	return false
}

var listRe = regexp.MustCompile(`There are (\d+)(?: of a max of (\d+))? players online:?\s*(.*)`)

// Players is the parsed result of `list`.
type Players struct {
	Online int
	Max    int
	Names  []string
}

// List asks the server for its player list.
func (c *Client) List(ctx context.Context) (Players, error) {
	out, err := c.Command(ctx, "list")
	if err != nil {
		return Players{}, err
	}
	return ParseList(out)
}

// ParseList reads the reply to `list`:
//
//	There are 2 of a max of 20 players online: Autumn, boyfriend
//	There are 0 of a max of 20 players online:
func ParseList(out string) (Players, error) {
	m := listRe.FindStringSubmatch(out)
	if m == nil {
		return Players{}, fmt.Errorf("unexpected list output: %q", strings.TrimSpace(out))
	}

	p := Players{}
	p.Online, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		p.Max, _ = strconv.Atoi(m[2])
	}
	for _, name := range strings.Split(m[3], ",") {
		if name = strings.TrimSpace(name); name != "" {
			p.Names = append(p.Names, name)
		}
	}
	return p, nil
}
