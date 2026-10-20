//go:build !windows

package process

import "testing"

func TestCodexExecutableIdentityCannotBeSuppressedByArguments(t *testing.T) {
	for _, test := range []struct {
		name, command, args string
		want                bool
	}{
		{"repository-path", "codex", "codex --cd /tmp/codex-switch", true},
		{"prompt", "Codex", "Codex explain codex-switch", true},
		{"desktop-argument", "ChatGPT", "ChatGPT codex-switch", true},
		{"normal", "codex", "codex app-server", true},
		{"unrelated", "editor", "editor /tmp/codex-switch", false},
		{"switcher", "codex-switch", "codex-switch current", false},
		{"bundle", "launcher", "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if isCodexProcess(test.command, test.args) != test.want {
				t.Fatal("executable identity classification changed")
			}
		})
	}
}
