package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, sk, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func authorized(s ssh.Signer) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
}

// fakeEnv is a minimal sshd: exec runs "echo <args>" or "exit <n>", and
// direct-tcpip connects to the requested address.
func fakeEnv(t *testing.T, hostKey ssh.Signer, clientKey ssh.PublicKey) net.Listener {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if m.User() == "dev" && string(k.Marshal()) == string(clientKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					switch nc.ChannelType() {
					case "session":
						ch, creqs, _ := nc.Accept()
						go func() {
							for r := range creqs {
								if r.Type != "exec" {
									r.Reply(false, nil)
									continue
								}
								r.Reply(true, nil)
								n := binary.BigEndian.Uint32(r.Payload)
								cmd := string(r.Payload[4 : 4+n])
								status := uint32(0)
								if after, ok := strings.CutPrefix(cmd, "echo "); ok {
									io.WriteString(ch, after+"\n")
								} else if after, ok := strings.CutPrefix(cmd, "exit "); ok {
									status = uint32(after[0] - '0')
									io.WriteString(ch.Stderr(), "bye\n")
								}
								ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{status}))
								ch.Close()
							}
						}()
					case "direct-tcpip":
						var p struct {
							Host  string
							Port  uint32
							OHost string
							OPort uint32
						}
						ssh.Unmarshal(nc.ExtraData(), &p)
						tc, err := net.Dial("tcp", net.JoinHostPort(p.Host, itoa(p.Port)))
						if err != nil {
							nc.Reject(ssh.ConnectionFailed, err.Error())
							continue
						}
						ch, creqs, _ := nc.Accept()
						go ssh.DiscardRequests(creqs)
						go func() { io.Copy(tc, ch); tc.Close() }()
						go func() { io.Copy(ch, tc); ch.Close() }()
					default:
						nc.Reject(ssh.UnknownChannelType, "")
					}
				}
			}()
		}
	}()
	return ln
}

func itoa(n uint32) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type setup struct {
	addr    string
	userKey ssh.Signer
	gwHost  ssh.PublicKey
}

func start(t *testing.T, phase devenvv1.Phase) setup {
	t.Helper()
	envHost, serverKey, gwHost, userKey := newSigner(t), newSigner(t), newSigner(t), newSigner(t)
	envLn := fakeEnv(t, envHost, serverKey.PublicKey())

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = devenvv1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		&devenvv1.DevEnvironment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: "work"},
			Spec: devenvv1.DevEnvironmentSpec{
				ImageRef: "base", Running: ptr.To(true),
				SSHAuthorizedKeys: []string{authorized(userKey)},
			},
			Status: devenvv1.DevEnvironmentStatus{Phase: phase, ServiceName: "work"},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: "work-ssh"},
			Data:       map[string][]byte{controller.HostPublicKeyFile: ssh.MarshalAuthorizedKey(envHost.PublicKey())},
		},
	).Build()

	gwLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := gwLn.Addr().String()
	gwLn.Close()
	gw := &Gateway{
		Addr:    addr,
		HostKey: gwHost,
		Dialer: &sshclient.Dialer{
			Client: c, Namespace: "devenv", Signer: serverKey,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, envLn.Addr().String())
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go gw.Start(ctx)
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return setup{addr: addr, userKey: userKey, gwHost: gwHost.PublicKey()}
}

func (s setup) dial(user string, key ssh.Signer) (*ssh.Client, error) {
	return ssh.Dial("tcp", s.addr, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.FixedHostKey(s.gwHost),
		Timeout:         5 * time.Second,
	})
}

func TestExecAndExitStatus(t *testing.T) {
	s := start(t, devenvv1.PhaseRunning)
	c, err := s.dial("work", s.userKey)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	sess, _ := c.NewSession()
	out, err := sess.Output("echo hello world")
	if err != nil || string(out) != "hello world\n" {
		t.Fatalf("exec = %q, %v", out, err)
	}

	sess, _ = c.NewSession()
	var stderr strings.Builder
	sess.Stderr = &stderr
	err = sess.Run("exit 3")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 3 {
		t.Fatalf("exit status: %v", err)
	}
	if stderr.String() != "bye\n" {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestLocalForward(t *testing.T) {
	s := start(t, devenvv1.PhaseRunning)
	target, _ := net.Listen("tcp", "127.0.0.1:0")
	defer target.Close()
	go func() {
		c, err := target.Accept()
		if err != nil {
			return
		}
		io.WriteString(c, "pong")
		c.Close()
	}()

	c, err := s.dial("work", s.userKey)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn, err := c.Dial("tcp", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(conn)
	if string(b) != "pong" {
		t.Errorf("forwarded = %q", b)
	}
}

func TestRejects(t *testing.T) {
	s := start(t, devenvv1.PhaseRunning)
	if _, err := s.dial("work", newSigner(t)); err == nil {
		t.Error("unknown key must be rejected")
	}
	if _, err := s.dial("other", s.userKey); err == nil {
		t.Error("unknown environment must be rejected")
	}
}

func TestStoppedEnvironmentMessage(t *testing.T) {
	s := start(t, devenvv1.PhaseStopped)
	c, err := s.dial("work", s.userKey)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	sess.Stderr = &stderr
	sess.Shell()
	sess.Wait()
	if !strings.Contains(stderr.String(), "not running") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
