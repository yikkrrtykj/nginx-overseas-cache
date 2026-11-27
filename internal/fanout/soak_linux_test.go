package fanout

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func readSoakOS() (soakOS, error) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return soakOS{}, err
	}
	_, tail, ok := strings.Cut(string(stat), ") ")
	fields := strings.Fields(tail)
	if !ok || len(fields) < 13 {
		return soakOS{}, errors.New("invalid proc stat")
	}
	user, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return soakOS{}, err
	}
	kernel, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return soakOS{}, err
	}
	pages, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return soakOS{}, err
	}
	for _, line := range strings.Split(string(pages), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		kb, err := strconv.ParseUint(strings.Fields(line)[1], 10, 64)
		if err != nil {
			return soakOS{}, err
		}
		fds, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			return soakOS{}, err
		}
		count := len(fds)
		// Linux reports proc CPU counters in USER_HZ, 100 on the amd64 runner.
		return soakOS{CPUSeconds: float64(user+kernel) / 100, RSSBytes: kb * 1024, OpenFDs: &count}, nil
	}
	return soakOS{}, errors.New("missing proc RSS")
}
