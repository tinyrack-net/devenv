package agent

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ListeningPort is a TCP port in LISTEN state inside the environment.
type ListeningPort struct {
	Port int `json:"port"`
	// Address is the bound address ("0.0.0.0", "127.0.0.1", "::").
	Address string `json:"address"`
	// Local reports a loopback-only bind (reachable only through tunnels).
	Local bool `json:"local"`
	// Process is the command name when the socket belongs to the dev user.
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

type PortList struct {
	Ports []ListeningPort `json:"ports"`
}

// ListPorts reads /proc/net/tcp{,6}. Ports used by the environment itself
// (sshd, VNC) are included; the server filters what it shows.
func ListPorts() (PortList, error) {
	owners := socketOwners()
	seen := map[string]bool{}
	out := PortList{Ports: []ListeningPort{}}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Scan() // header
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || fields[3] != "0A" { // 0A = LISTEN
				continue
			}
			addr, port, err := parseHexAddr(fields[1])
			if err != nil {
				continue
			}
			key := fmt.Sprintf("%s:%d", addr, port)
			if seen[key] {
				continue
			}
			seen[key] = true
			p := ListeningPort{Port: port, Address: addr.String(), Local: addr.IsLoopback()}
			if o, ok := owners[fields[9]]; ok {
				p.PID, p.Process = o.pid, o.comm
			}
			out.Ports = append(out.Ports, p)
		}
		fh.Close()
	}
	sort.Slice(out.Ports, func(i, j int) bool {
		if out.Ports[i].Port != out.Ports[j].Port {
			return out.Ports[i].Port < out.Ports[j].Port
		}
		return out.Ports[i].Address < out.Ports[j].Address
	})
	return out, nil
}

func parseHexAddr(s string) (net.IP, int, error) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return nil, 0, err
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, 0, err
	}
	// /proc stores the address as host-order 32 bit words.
	for i := 0; i+4 <= len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	ip := net.IP(b)
	if v4 := ip.To4(); v4 != nil && len(b) == 16 && ip.IsUnspecified() {
		ip = net.IPv6unspecified
	}
	return ip, int(port), nil
}

type owner struct {
	pid  int
	comm string
}

// socketOwners maps socket inodes to processes we can inspect (own user).
func socketOwners() map[string]owner {
	out := map[string]owner{}
	dirs, _ := filepath.Glob("/proc/[0-9]*/fd")
	for _, d := range dirs {
		pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(d)))
		fds, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		var comm string
		for _, fd := range fds {
			l, err := os.Readlink(filepath.Join(d, fd.Name()))
			if err != nil || !strings.HasPrefix(l, "socket:[") {
				continue
			}
			if comm == "" {
				b, _ := os.ReadFile(filepath.Join(filepath.Dir(d), "comm"))
				comm = strings.TrimSpace(string(b))
			}
			out[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] = owner{pid: pid, comm: comm}
		}
	}
	return out
}
