package main

import (
	"flag"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestCLIEntryPointHelper executes the real command dispatcher in a subprocess.
func TestCLIEntryPointHelper(t *testing.T) {
	if os.Getenv("CHILL_CLI_ENTRY_TEST") != "1" {
		return
	}
	start := slices.Index(os.Args, "--")
	os.Args = append([]string{os.Args[0]}, os.Args[start+1:]...)
	flag.CommandLine = flag.NewFlagSet("chill", flag.ExitOnError)
	main()
	os.Exit(0)
}

// TestInformationalAndUnknownCommandsDoNotStartDaemon checks CLI aliases and typos before startup.
func TestInformationalAndUnknownCommandsDoNotStartDaemon(t *testing.T) {
	withConfigDir(t)
	t.Setenv("CHILL_CLI_ENTRY_TEST", "1")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("PATH", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want string
		fail bool
	}{
		{[]string{"help"}, "Commands:", false},
		{[]string{"version"}, "chill ", false},
		{[]string{"list"}, "lofi-girl", false},
		{[]string{"hlep"}, "unknown station: hlep", true},
		{[]string{"--station", "unknown"}, "unknown station: unknown", true},
		{[]string{"help", "extra"}, "usage: chill help", true},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			args := append([]string{"-test.run=^TestCLIEntryPointHelper$", "--"}, test.args...)
			out, err := exec.CommandContext(t.Context(), exe, args...).CombinedOutput()
			if (err != nil) != test.fail || !strings.Contains(string(out), test.want) {
				t.Fatalf("output %q, error %v", out, err)
			}
			for _, path := range []string{socketPath(), daemonLogPath()} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("command attempted daemon startup: %s (%v)", path, err)
				}
			}
		})
	}
}
