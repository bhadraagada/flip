package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBackgroundProcessHasNoConsole(t *testing.T) {
	if os.Getenv("FLIP_TEST_NO_CONSOLE") == "1" {
		window, _, _ := kernel.NewProc("GetConsoleWindow").Call()
		if window != 0 {
			os.Exit(2)
		}
		os.Stdout.WriteString("no console")
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestBackgroundProcessHasNoConsole$")
	cmd.Env = append(os.Environ(), "FLIP_TEST_NO_CONSOLE=1")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "no console" {
		t.Fatalf("background child acquired a console: %v %s", err, out)
	}
}
