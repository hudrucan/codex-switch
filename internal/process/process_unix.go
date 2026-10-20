//go:build !windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Info struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

func DetectCodex() ([]Info, error) {
	output, err := exec.Command("ps", "-axo", "pid=,comm=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect running processes: %w", err)
	}
	self := os.Getpid()
	var matches []Info
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == self {
			continue
		}
		command := filepath.Base(fields[1])
		args := strings.Join(fields[2:], " ")
		if isCodexProcess(command, args) {
			matches = append(matches, Info{PID: pid, Command: command})
		}
	}
	return matches, nil
}

func isCodexProcess(command, args string) bool {
	if command == "codex" || command == "Codex" || command == "ChatGPT" {
		return true
	}
	return strings.Contains(args, "/ChatGPT.app/Contents/MacOS/ChatGPT")
}
