package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// keyFile writes an OpenSSH-format private key and returns its path and signer.
func keyFile(t *testing.T) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "outpost")
	// The OpenSSH format, which is what an ssh-keygen key is and what the parser
	// has to accept in production.
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, signer
}

// The container is the reason this package stopped shelling out: there is no ssh
// binary in the image, so the key has to be read from a path, not a home folder.
func TestKeyIsReadFromAPath(t *testing.T) {
	path, _ := keyFile(t)
	r := New("root", path)
	if _, err := r.signer(); err != nil {
		t.Fatalf("signer: %v", err)
	}
}

func TestAMissingKeyNamesThePathAndTheContainer(t *testing.T) {
	r := New("root", filepath.Join(t.TempDir(), "absent"))
	_, err := r.signer()
	if err == nil {
		t.Fatal("a missing key should be an error")
	}
	if !strings.Contains(err.Error(), "absent") || !strings.Contains(err.Error(), "data folder") {
		t.Fatalf("error = %q, want the path and where a container looks", err)
	}
}

func TestNoKeyConfiguredSaysWhichSetting(t *testing.T) {
	_, err := New("root", "").signer()
	if err == nil || !strings.Contains(err.Error(), "ssh_private_key") {
		t.Fatalf("error = %q", err)
	}
}

func TestExpandPathHandlesATilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	got, err := expandPath("~/.ssh/outpost")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, ".ssh/outpost") {
		t.Fatalf("expandPath = %q", got)
	}
	// An absolute path is left alone, and so is anything else that only looks like
	// a tilde path.
	for _, p := range []string{"/etc/outpost/key", "keys/outpost", "~notahome/x"} {
		if got, err := expandPath(p); err != nil || got != p {
			t.Errorf("expandPath(%q) = %q, %v", p, got, err)
		}
	}
}

// --- against a real SSH server -------------------------------------------

// startSSH runs an in-process SSH server that answers every exec with the given
// handler, and points sshPort at it.
func startSSH(t *testing.T, handler func(cmd string) (string, uint32)) (string, ssh.Signer) {
	t.Helper()

	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	old := sshPort
	sshPort = port
	t.Cleanup(func() { sshPort = old })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveOne(conn, cfg, handler, t)
		}
	}()

	keyPath, clientSigner := keyFile(t)
	return keyPath, clientSigner
}

func serveOne(conn net.Conn, cfg *ssh.ServerConfig, handler func(cmd string) (string, uint32), t *testing.T) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only sessions here")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			return
		}
		go func() {
			defer ch.Close()
			for req := range chReqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				// The payload is a length-prefixed string: the command.
				payload := req.Payload
				if len(payload) < 4 {
					_ = req.Reply(false, nil)
					return
				}
				n := binary.BigEndian.Uint32(payload[:4])
				cmd := string(payload[4 : 4+n])
				out, code := handler(cmd)
				_ = req.Reply(true, nil)

				// Output first, then the exit status: the status is what tells the
				// client the command is over, and real sshd does not send it until the
				// output is on the wire.
				if out != "" {
					if n, werr := ch.Write([]byte(out)); werr != nil {
						t.Logf("fake server write failed after %d bytes: %v", n, werr)
					}
				}
				// EOF first: closing a channel without it can drop the data that was
				// just written, which is not what sshd does.
				_ = ch.CloseWrite()
				status := make([]byte, 4)
				binary.BigEndian.PutUint32(status, code)
				_, _ = ch.SendRequest("exit-status", false, status)
				// Give the mux a moment to put the bytes on the wire before the
				// deferred Close. sshd has a process exit to do this for it; a fake
				// does not, and closing in the same instruction loses the data
				// often enough to make the test flaky.
				time.Sleep(20 * time.Millisecond)
				return
			}
		}()
	}
}

func TestRunSendsTheCommandOverSSH(t *testing.T) {
	var got string
	keyPath, _ := startSSH(t, func(cmd string) (string, uint32) {
		got = cmd
		return "hello from the vps\n", 0
	})

	out, err := New("root", keyPath).Run(context.Background(), "127.0.0.1", "tar -czf world.tar.gz world")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "hello from the vps\n" {
		t.Fatalf("output = %q", out)
	}
	if got != "tar -czf world.tar.gz world" {
		t.Fatalf("the server received %q", got)
	}
}

// "rclone said no" is the only useful thing in the log when a save fails, so a
// non-zero exit has to carry the remote output rather than just the exit code.
func TestAFailedCommandCarriesItsOutput(t *testing.T) {
	keyPath, _ := startSSH(t, func(string) (string, uint32) {
		return "rclone: 501 NotImplemented\n", 1
	})

	out, err := New("root", keyPath).Run(context.Background(), "127.0.0.1", "rclone copy")
	if err == nil {
		t.Fatal("a non-zero exit should be an error")
	}
	if !strings.Contains(out, "501 NotImplemented") || !strings.Contains(err.Error(), "501 NotImplemented") {
		t.Fatalf("out = %q, err = %q", out, err)
	}
}

func TestReadyIsFalseWhenNothingListens(t *testing.T) {
	// A port that is closed: Ready is the pre-flight while a VPS is booting, and
	// its whole job is to answer false rather than fail loudly.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	old := sshPort
	sshPort = port
	defer func() { sshPort = old }()

	if New("root", filepath.Join(t.TempDir(), "absent")).Ready(context.Background(), "127.0.0.1") {
		t.Fatal("Ready on a closed port")
	}
}

func TestRunStopsWhenTheContextDoes(t *testing.T) {
	// A server that accepts and never answers: the caller's deadline has to end the
	// call, or a wedged VPS holds the orchestrator's shutdown path for ever.
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	old := sshPort
	sshPort = port
	defer func() { sshPort = old }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Handshake, accept the session, then never answer the exec request: the
		// client is left waiting exactly like a wedged VPS.
		_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for newChan := range chans {
			ch, chReqs, err := newChan.Accept()
			if err != nil {
				return
			}
			_ = ch
			for range chReqs {
				// Never reply.
			}
		}
	}()

	keyPath, _ := keyFile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = New("root", keyPath).Run(ctx, "127.0.0.1", "sleep 600")
	if err == nil {
		t.Fatal("a silent server should end at the deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("error = %q, want the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s to give up", elapsed)
	}
}
