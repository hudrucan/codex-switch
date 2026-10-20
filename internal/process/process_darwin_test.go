//go:build darwin

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestDarwinPSPreservesGUIAndTerminalExecutableNames(t *testing.T) {
	// Exercise the actual macOS column widths without inspecting other processes
	// or launching Codex. Each child is an isolated copy of the system sleep tool.
	sleep, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "synthetic application with spaces")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ChatGPT", "Codex", "codex"} {
		t.Run(name, func(t *testing.T) {
			binary := filepath.Join(root, name)
			if err := os.WriteFile(binary, sleep, 0o700); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(binary, "30")
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = child.Process.Kill()
				_ = child.Wait()
			})
			output, err := exec.Command("ps", "-p", strconv.Itoa(child.Process.Pid), "-o", psColumns).Output()
			if err != nil {
				t.Fatal(err)
			}
			got := parseCodexProcesses(output, os.Getpid())
			want := []Info{{PID: child.Process.Pid, Command: name}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("process identity lost in ps output: got %+v, want %+v", got, want)
			}
		})
	}
}
