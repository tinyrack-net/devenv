package agent

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// cgroup v2 files of the container (the pod's memory limit applies here).
const cgroupDir = "/sys/fs/cgroup"

func readUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// oomKills returns the container's OOM kill counter (0 when unavailable).
func oomKills() uint64 {
	f, err := os.Open(cgroupDir + "/memory.events")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if ok && k == "oom_kill" {
			n, _ := strconv.ParseUint(v, 10, 64)
			return n
		}
	}
	return 0
}

// Stats reports disk usage of path and the container's memory usage.
func Stats(req StatsRequest) (StatsResult, error) {
	p := req.Path
	if p == "" {
		p = homeDir()
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(p, &fs); err != nil {
		return StatsResult{}, err
	}
	res := StatsResult{
		DiskTotal: fs.Blocks * uint64(fs.Bsize),
		DiskUsed:  (fs.Blocks - fs.Bfree) * uint64(fs.Bsize),
		OOMKills:  oomKills(),
	}
	if v, ok := readUint(cgroupDir + "/memory.current"); ok {
		res.MemoryUsed = v
	}
	if v, ok := readUint(cgroupDir + "/memory.max"); ok {
		res.MemoryLimit = v
	}
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, e := range entries {
			if _, err := strconv.Atoi(e.Name()); err == nil {
				res.Processes++
			}
		}
	}
	return res, nil
}
