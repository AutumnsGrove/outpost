package rcon

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

// fakeServer speaks just enough RCON for the client: login, then scripted
// replies. replies maps a command to the payloads of its response packets; a
// command that maps to a single "" is the empty reply vanilla sends for `say`.
func fakeServer(t *testing.T, password string, replies map[string][]string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(conn, password, replies)
		}
	}()
	return ln.Addr().String()
}

func serve(conn net.Conn, password string, replies map[string][]string) {
	defer conn.Close()
	for {
		var size int32
		if err := binary.Read(conn, binary.LittleEndian, &size); err != nil {
			return
		}
		raw := make([]byte, size)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		id := int32(binary.LittleEndian.Uint32(raw[0:4]))
		typ := int32(binary.LittleEndian.Uint32(raw[4:8]))
		body := strings.TrimRight(string(raw[8:]), "\x00")

		switch typ {
		case typeAuth:
			if body != password {
				id = -1
			}
			// Real servers answer a login with type 2, not 3.
			send(conn, id, typeCommand, "")
		case typeCommand:
			parts, ok := replies[body]
			if !ok {
				parts = []string{"Unknown command"}
			}
			for _, p := range parts {
				send(conn, id, typeResponseValue, p)
			}
		}
	}
}

func send(conn net.Conn, id, typ int32, body string) {
	buf := binary.LittleEndian.AppendUint32(nil, uint32(4+4+len(body)+2))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(id))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(typ))
	buf = append(buf, body...)
	buf = append(buf, 0, 0)
	_, _ = conn.Write(buf)
}

func TestWrongPasswordIsNamedAsSuch(t *testing.T) {
	addr := fakeServer(t, "right", nil)
	_, err := Dial(context.Background(), addr, "wrong")
	if err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("Dial with a bad password = %v, want a wrong-password error", err)
	}
}

func TestCommandWithAnEmptyReplyIsSuccess(t *testing.T) {
	addr := fakeServer(t, "pw", map[string][]string{"say hi": {""}})
	c, err := Dial(context.Background(), addr, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	out, err := c.Command(context.Background(), "say hi")
	if err != nil {
		t.Fatalf("an empty reply is how vanilla says OK, got error: %v", err)
	}
	if out != "" {
		t.Fatalf("output = %q", out)
	}
}

func TestCommandJoinsAFragmentedReply(t *testing.T) {
	addr := fakeServer(t, "pw", map[string][]string{"help": {"one ", "two ", "three"}})
	c, err := Dial(context.Background(), addr, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	out, err := c.Command(context.Background(), "help")
	if err != nil || out != "one two three" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
