package mcpserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/api"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/config"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/sshclient"
	"github.com/tinyrack-net/devenv/internal/tokens"
	"github.com/tinyrack-net/devenv/internal/users"
)

const testToken = "dvm_test-token"

func signer(t *testing.T) ssh.Signer {
	_, sk, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// localSSHD executes exec requests on the test machine with bash, so the
// real command wrapper (setsid bash -lc ...) is exercised.
func localSSHD(t *testing.T, host ssh.Signer, client ssh.PublicKey) string {
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) == string(client.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(host)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
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
					ch, creqs, _ := nc.Accept()
					go func() {
						var cmd *exec.Cmd
						for r := range creqs {
							switch r.Type {
							case "subsystem":
								if !strings.HasSuffix(string(r.Payload), "sftp") {
									r.Reply(false, nil)
									continue
								}
								r.Reply(true, nil)
								srv, err := sftp.NewServer(ch)
								if err != nil {
									ch.Close()
									return
								}
								go func() { _ = srv.Serve(); srv.Close(); ch.Close() }()
							case "exec":
								n := binary.BigEndian.Uint32(r.Payload)
								line := string(r.Payload[4 : 4+n])
								cmd = exec.Command("sh", "-c", line)
								cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
								// Like OpenSSH: the command is a session leader.
								cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
								r.Reply(true, nil)
								if err := cmd.Start(); err != nil {
									ch.Close()
									return
								}
								go func() {
									code := 0
									if err := cmd.Wait(); err != nil {
										var ee *exec.ExitError
										if errors.As(err, &ee) {
											code = ee.ExitCode()
										}
									}
									ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
									ch.Close()
								}()
							case "signal":
								if cmd != nil && cmd.Process != nil {
									// setsid made the child a group leader.
									syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
									cmd.Process.Kill()
								}
							default:
								r.Reply(false, nil)
							}
						}
						if cmd != nil && cmd.Process != nil {
							syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String()
}

var helperBin string

// buildHelper compiles devenv-agent once per test binary.
func buildHelper(t *testing.T) string {
	t.Helper()
	if helperBin == "" {
		dir, err := os.MkdirTemp("", "devenv-agent-test")
		if err != nil {
			t.Fatal(err)
		}
		bin := dir + "/devenv-agent"
		if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/devenv-agent").CombinedOutput(); err != nil {
			t.Fatalf("build helper: %v\n%s", err, out)
		}
		helperBin = bin
	}
	return helperBin
}

type harness struct {
	srv     *Server
	t       *testing.T
	c       client.Client
	session *mcp.ClientSession
	url     string
}

func q(s string) *resource.Quantity { v := resource.MustParse(s); return &v }

func newHarness(t *testing.T) *harness {
	t.Helper()
	// The test machine has no /home/dev and no /opt/devenv: use a temp home
	// and the helper built from this tree. A private TMPDIR isolates the
	// helper daemon of each test.
	oldHome, oldHelper := homeDir, helperPath
	homeDir, helperPath = t.TempDir(), buildHelper(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Cleanup(func() {
		_ = exec.Command("pkill", "-f", helperPath+" daemon").Run()
		homeDir, helperPath = oldHome, oldHelper
	})
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = devenvv1.AddToScheme(s)
	envHost, serverKey := signer(t), signer(t)
	sshdAddr := localSSHD(t, envHost, serverKey.PublicKey())

	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&devenvv1.DevEnvironment{}, &devenvv1.DevUser{}).WithObjects(
		&devenvv1.DevUser{ObjectMeta: metav1.ObjectMeta{Name: "admin"}, Spec: devenvv1.DevUserSpec{Role: devenvv1.RoleAdmin}},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("16Gi")},
				Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			},
		},
		&devenvv1.DevEnvironmentClass{ObjectMeta: metav1.ObjectMeta{Name: "default"}, Spec: devenvv1.DevEnvironmentClassSpec{Limits: devenvv1.Limits{MaxCPU: q("4")}}},
		&devenvv1.DevImage{ObjectMeta: metav1.ObjectMeta{Name: "base"}, Spec: devenvv1.DevImageSpec{Image: "example.com/base:1", DefaultResources: devenvv1.Resources{CPU: q("1"), Memory: q("1Gi")}}},
	).Build()

	sessions, _ := auth.NewSessions([]byte("k"), false)
	dialer := &sshclient.Dialer{
		Client: c, Namespace: "devenv", Signer: serverKey,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sshdAddr)
		},
	}
	cfg := &config.Config{Auth: config.Auth{Password: config.Password{Password: "pw"}}}
	cfg.MCP.LegacyToken.Token = testToken
	cfg.Defaults()
	apiSrv := &api.Server{
		Client: c, Reader: c, Namespace: "devenv", Config: cfg, Sessions: sessions,
		Users:    &users.Store{Client: c, Config: cfg},
		Tokens:   &tokens.Store{Client: c, Namespace: "devenv-system"},
		Capacity: &capacity.Checker{Reader: c, Namespace: "devenv"}, Dialer: dialer,
	}
	m := &Server{API: apiSrv, Dialer: dialer, Resolver: apiSrv, Activity: &activity.Log{}}
	ts := httptest.NewServer(m.Handler())
	t.Cleanup(ts.Close)

	// A fake "controller": environments become Running immediately, with the
	// host key Secret the dialer needs.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			list := &devenvv1.DevEnvironmentList{}
			_ = c.List(ctx, list)
			for i := range list.Items {
				e := &list.Items[i]
				want := devenvv1.PhaseStopped
				if e.IsRunning() {
					want = devenvv1.PhaseRunning
				}
				if e.Status.Phase != want || e.Status.ObservedGeneration != e.Generation {
					e.Status.Phase, e.Status.ServiceName, e.Status.ObservedGeneration = want, e.Name, e.Generation
					_ = c.Status().Update(ctx, e)
				}
				sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: e.Name + "-ssh"},
					Data: map[string][]byte{controller.HostPublicKeyFile: ssh.MarshalAuthorizedKey(envHost.PublicKey())}}
				_ = c.Create(ctx, sec)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	h := &harness{t: t, c: c, url: ts.URL, srv: m}
	cl := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	sess, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: bearer{testToken}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	h.session = sess
	return h
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// connectAs opens a second MCP session with a personal token of user.
func (h *harness) connectAs(user string, role devenvv1.Role) *harness {
	h.t.Helper()
	_ = h.c.Create(context.Background(), &devenvv1.DevUser{ObjectMeta: metav1.ObjectMeta{Name: user}, Spec: devenvv1.DevUserSpec{Role: role}})
	_, plain, err := h.srv.API.Tokens.Create(context.Background(), user, "agent", []string{tokens.ScopeMCP}, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-" + user}, nil)
	sess, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: h.url, HTTPClient: &http.Client{Transport: bearer{plain}},
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { sess.Close() })
	out := *h
	out.session = sess
	return &out
}

// call invokes a tool and decodes its structured output.
func (h *harness) call(name string, args map[string]any, out any) (isError bool, text string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if out != nil && !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			h.t.Fatalf("%s: decode %s: %v", name, b, err)
		}
	}
	return res.IsError, text
}

func TestAuth(t *testing.T) {
	h := newHarness(t)
	for _, tok := range []string{"", "Bearer wrong", "Basic " + testToken} {
		req, _ := http.NewRequest("POST", h.url, strings.NewReader(`{}`))
		if tok != "" {
			req.Header.Set("Authorization", tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("auth %q: status %d", tok, resp.StatusCode)
		}
	}
}

func TestToolsListed(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"list_images", "list_classes", "get_capacity", "list_environments", "get_environment",
		"create_environment", "update_environment", "start_environment", "stop_environment", "delete_environment", "restart_environment", "exec",
		"read_file", "write_file", "edit_file", "list_dir", "search", "find_files", "start_process", "read_process_output", "list_processes", "stop_process"} {
		if !got[want] {
			t.Errorf("tool %s missing", want)
		}
	}
}

func TestLifecycleAndExec(t *testing.T) {
	h := newHarness(t)

	var imgs listImagesOut
	h.call("list_images", nil, &imgs)
	if len(imgs.Images) != 1 || imgs.Images[0].Name != "base" {
		t.Fatalf("images = %+v", imgs)
	}

	if isErr, text := h.call("create_environment", map[string]any{"name": "agent1", "image": "base", "cpu": "16"}, nil); !isErr || !strings.Contains(text, "exceeds class limit") {
		t.Errorf("limit violation: %v %s", isErr, text)
	}
	if isErr, text := h.call("create_environment", map[string]any{"name": "Bad Name", "image": "base"}, nil); !isErr || !strings.Contains(text, "name") {
		t.Errorf("bad name: %v %s", isErr, text)
	}

	var created envOut
	if isErr, text := h.call("create_environment", map[string]any{"name": "agent1", "image": "base", "memory": "2Gi"}, &created); isErr {
		t.Fatalf("create: %s", text)
	}
	if created.Environment.Phase != "Running" || !created.Environment.CreatedByMCP {
		t.Errorf("created = %+v", created.Environment)
	}
	obj := &devenvv1.DevEnvironment{}
	_ = h.c.Get(context.Background(), client.ObjectKey{Namespace: "devenv", Name: "agent1"}, obj)
	if obj.Spec.Owner != "admin" || obj.Labels[LabelCreatedBy] != "mcp" {
		t.Errorf("owner/labels = %q %v", obj.Spec.Owner, obj.Labels)
	}

	var out execOut
	if isErr, text := h.call("exec", map[string]any{"name": "agent1", "command": "echo hello; echo oops >&2; exit 3"}, &out); isErr {
		t.Fatalf("exec: %s", text)
	}
	if out.ExitCode != 3 || out.Stdout != "hello\n" || out.Stderr != "oops\n" || out.TimedOut {
		t.Errorf("exec = %+v", out)
	}

	h.call("exec", map[string]any{"name": "agent1", "command": `printf "%s|%s" "$FOO" "$(cat)"`, "env": map[string]string{"FOO": "it's"}, "stdin": "piped"}, &out)
	if out.Stdout != "it's|piped" {
		t.Errorf("env/stdin = %q", out.Stdout)
	}
	if isErr, _ := h.call("exec", map[string]any{"name": "agent1", "command": "true", "env": map[string]string{"BAD-NAME": "x"}}, nil); !isErr {
		t.Error("invalid env name accepted")
	}

	start := time.Now()
	marker := homeDir + "/child.pid"
	h.call("exec", map[string]any{"name": "agent1", "command": "sleep 300 & echo $! > " + marker + "; sleep 300", "timeout_seconds": 1}, &out)
	if !out.TimedOut || out.ExitCode != -1 || time.Since(start) > 10*time.Second {
		t.Errorf("timeout = %+v after %v", out, time.Since(start))
	}
	// Regression: the timeout must kill the command and its children.
	time.Sleep(300 * time.Millisecond)
	if b, err := os.ReadFile(marker); err == nil {
		if exec.Command("kill", "-0", strings.TrimSpace(string(b))).Run() == nil {
			t.Errorf("child %s survived the timeout", strings.TrimSpace(string(b)))
		}
	} else {
		t.Errorf("marker: %v", err)
	}

	h.call("exec", map[string]any{"name": "agent1", "command": "head -c 600000 /dev/zero | tr '\\0' a"}, &out)
	if !out.Truncated || len(out.Stdout) > maxStreamBytes+100 || !strings.Contains(out.Stdout, "bytes omitted") {
		t.Errorf("truncation: truncated=%v len=%d", out.Truncated, len(out.Stdout))
	}

	// Stopped environments are started by exec.
	var stopped envOut
	h.call("stop_environment", map[string]any{"name": "agent1"}, &stopped)
	if stopped.Environment.Phase != "Stopped" {
		t.Errorf("stop = %+v", stopped.Environment)
	}
	if isErr, _ := h.call("exec", map[string]any{"name": "agent1", "command": "true", "auto_start": false}, nil); !isErr {
		t.Error("exec on stopped env without auto_start should fail")
	}
	h.call("exec", map[string]any{"name": "agent1", "command": "echo back"}, &out)
	if out.Stdout != "back\n" {
		t.Errorf("auto start exec = %+v", out)
	}

	var upd envOut
	if isErr, text := h.call("update_environment", map[string]any{"name": "agent1", "cpu": "2"}, &upd); isErr {
		t.Fatalf("update: %s", text)
	}
	if upd.Environment.Resources.CPU != "2" || upd.Environment.Resources.Memory != "2Gi" || upd.Note == "" {
		t.Errorf("update = %+v", upd)
	}

	var list listEnvsOut
	h.call("list_environments", map[string]any{"created_by_mcp_only": true}, &list)
	if len(list.Environments) != 1 {
		t.Errorf("list = %+v", list)
	}

	var del deleteOut
	h.call("delete_environment", map[string]any{"name": "agent1"}, &del)
	if isErr, text := h.call("get_environment", map[string]any{"name": "agent1"}, nil); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("after delete: %v %s", isErr, text)
	}
}

func TestBuildCommand(t *testing.T) {
	cmd, err := buildCommand(execIn{Command: `printf '%s|%s|' "$A" "$B"; pwd`, Cwd: t.TempDir(), Env: map[string]string{"B": "two words", "A": "o'k"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "exec bash -lc ") {
		t.Fatalf("cmd = %s", cmd)
	}
	// Quoting must survive a real shell.
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "o'k|two words|/") {
		t.Errorf("output = %q", out)
	}
}

func TestPerUserTokens(t *testing.T) {
	h := newHarness(t)
	alice := h.connectAs("alice", devenvv1.RoleUser)
	bob := h.connectAs("bob", devenvv1.RoleUser)

	var out envOut
	if isErr, text := alice.call("create_environment", map[string]any{"name": "a1", "image": "base"}, &out); isErr {
		t.Fatal(text)
	}
	if out.Environment.Owner != "alice" {
		t.Fatalf("owner %q", out.Environment.Owner)
	}
	var list listEnvsOut
	bob.call("list_environments", map[string]any{}, &list)
	if len(list.Environments) != 0 {
		t.Fatalf("bob sees %+v", list)
	}
	if isErr, text := bob.call("exec", map[string]any{"name": "a1", "command": "true"}, nil); !isErr || !strings.Contains(text, "not found") {
		t.Fatalf("bob exec on alice's env: %v %s", isErr, text)
	}
	if isErr, _ := bob.call("delete_environment", map[string]any{"name": "a1"}, nil); !isErr {
		t.Fatal("bob deleted alice's env")
	}
	var ex execOut
	if isErr, text := alice.call("exec", map[string]any{"name": "a1", "command": "echo hi-alice"}, &ex); isErr || !strings.Contains(ex.Stdout, "hi-alice") {
		t.Fatalf("alice exec: %s %+v", text, ex)
	}
	// The legacy token acts as the admin, who sees everything with all.
	h.call("list_environments", map[string]any{"all": true}, &list)
	if len(list.Environments) != 1 {
		t.Fatalf("admin all: %+v", list)
	}
	// Tool calls are in the activity log with user and summary.
	entries := h.srv.Activity.List("a1", 10, nil)
	var found bool
	for _, e := range entries {
		if e.Tool == "exec" && e.User == "alice" && e.Summary == "echo hi-alice" && strings.Contains(e.Detail, "hi-alice") && !e.Running {
			found = true
		}
	}
	if !found {
		t.Fatalf("activity %+v", entries)
	}
}
