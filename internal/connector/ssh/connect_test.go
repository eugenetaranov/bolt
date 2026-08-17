package ssh

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startTestSSHServer starts a minimal real SSH server on loopback that
// only accepts password auth with the given password (no channels are
// serviced — this only needs to complete the auth handshake, which is
// all Connect exercises). Returns the listener address.
func startTestSSHServer(t *testing.T, password string) string {
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
				for newChannel := range chans {
					_ = newChannel.Reject(ssh.Prohibited, "no channels serviced by this test server")
				}
			}()
		}
	}()

	return listener.Addr().String()
}

func TestConnect_PasswordPromptFallback_Success(t *testing.T) {
	// No key/agent auth available, so buildAuthMethods offers only the
	// password-prompt fallback.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	addr := startTestSSHServer(t, "correct-horse-battery-staple")
	host, port := splitTestAddr(t, addr)

	var promptCalls int
	c := New(host,
		WithPort(port),
		WithUser("anyuser"),
		WithInsecureHostKey(),
		WithPasswordPrompt(func() (string, error) {
			promptCalls++
			return "correct-horse-battery-staple", nil
		}),
	)

	err := c.Connect(context.Background())
	require.NoError(t, err, "Connect should succeed via the lazy password-prompt fallback")
	assert.Equal(t, 1, promptCalls, "the prompt should fire exactly once")
	_ = c.Close()
}

func TestConnect_PasswordPromptFallback_WrongPasswordFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	addr := startTestSSHServer(t, "correct-horse-battery-staple")
	host, port := splitTestAddr(t, addr)

	c := New(host,
		WithPort(port),
		WithUser("anyuser"),
		WithInsecureHostKey(),
		WithPasswordPrompt(func() (string, error) { return "wrong-password", nil }),
	)

	err := c.Connect(context.Background())
	require.Error(t, err, "Connect should fail when the prompted password is rejected by the server")
}

func TestConnect_NoPasswordNoPromptFailsFastWithoutDialing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	// No WithPasswordPrompt at all: buildAuthMethods yields zero methods,
	// so Connect must fail before ever dialing -- point at a port nothing
	// is listening on to prove that (a real dial attempt would time out
	// instead of failing immediately).
	c := New("127.0.0.1", WithPort(1))

	err := c.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no SSH authentication methods available")
}

// splitTestAddr splits a "host:port" listener address into host and int port.
func splitTestAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}
