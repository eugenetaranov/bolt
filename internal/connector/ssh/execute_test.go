package ssh

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startExecSSHServer starts a real loopback SSH server that services one exec
// per session. drainStdin controls the behaviour under test:
//
//   - drainStdin=false reproduces a *fast* command: the server sends exit-status
//     0 and closes the channel WITHOUT reading stdin. A client that copies its
//     stdin via session.Stdin then races the channel close and Run surfaces the
//     copy's io.EOF as a spurious error. This is the regression guarded here.
//   - drainStdin=true reads all of stdin first and publishes it on the returned
//     channel, so a test can assert the sudo password was actually delivered.
//
// The returned channel receives the bytes the server read from stdin (nil-safe:
// it is only sent to when drainStdin is true).
func startExecSSHServer(t *testing.T, password string, drainStdin bool) (addr string, stdinCh <-chan []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)

	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })

	captured := make(chan []byte, 1)

	sendExit := func(ch ssh.Channel, status uint32) {
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
	}

	serveChannel := func(newCh ssh.NewChannel) {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session")
			return
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			return
		}
		for req := range requests {
			if req.Type != "exec" {
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
				continue
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			if drainStdin {
				// Blocks until the client closes its stdin write side, so we
				// see exactly what was fed (the sudo password).
				data, _ := io.ReadAll(ch)
				captured <- data
			}
			// Exit immediately. In the fast-command case this closes the
			// channel while the client may still be writing the password.
			sendExit(ch, 0)
			_ = ch.Close()
			return
		}
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sshConn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					serveChannel(newCh)
				}
			}()
		}
	}()

	return listener.Addr().String(), captured
}

// newSudoConnector dials the given test server as a connected connector with
// sudo + password enabled, so Execute wraps commands with sudo and feeds the
// password over stdin.
func newSudoConnector(t *testing.T, addr, password string) *Connector {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	host, port := splitTestAddr(t, addr)
	c := New(host,
		WithPort(port),
		WithUser("anyuser"),
		WithInsecureHostKey(),
		WithPassword(password),
	)
	require.NoError(t, c.Connect(context.Background()))
	t.Cleanup(func() { _ = c.Close() })
	c.SetSudo(true, password)
	return c
}

// TestExecute_SudoFastCommandNoEOF is the regression guard: a sudo command that
// exits before consuming stdin (like the iptables availability probe) must not
// fail with "failed to execute command: EOF". Before the StdinPipe fix, feeding
// the password via session.Stdin made Run return the stdin-copy's io.EOF when
// the channel closed before the copy finished.
//
// That is a race between the client's stdin write and the server's channel
// close, so a single Execute can get lucky. We run many fast-exit sessions on
// one connection (the real pattern: many tasks reuse a connection) — the buggy
// path reliably loses the race at least once, while the fixed path stays green.
func TestExecute_SudoFastCommandNoEOF(t *testing.T) {
	addr, _ := startExecSSHServer(t, "s3cr3t", false /* do not drain stdin */)
	c := newSudoConnector(t, addr, "s3cr3t")

	for i := 0; i < 50; i++ {
		res, err := c.Execute(context.Background(), "command -v iptables")
		require.NoErrorf(t, err, "iteration %d: a fast sudo command must not surface a spurious stdin EOF", i)
		assert.Equal(t, 0, res.ExitCode)
	}
}

// TestExecute_SudoPasswordDeliveredOverStdin proves the password actually
// reaches the process on stdin (and only there — never in the command/argv).
func TestExecute_SudoPasswordDeliveredOverStdin(t *testing.T) {
	addr, stdinCh := startExecSSHServer(t, "s3cr3t", true /* drain and capture stdin */)
	c := newSudoConnector(t, addr, "s3cr3t")

	_, err := c.Execute(context.Background(), "whoami")
	require.NoError(t, err)

	select {
	case got := <-stdinCh:
		assert.Equal(t, "s3cr3t\n", string(got), "the sudo password must be delivered on stdin")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server to receive stdin")
	}
}
