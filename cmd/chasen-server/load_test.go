package main

import (
	"io"
	"os"
	"regexp"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Run("prints the load, the memory, and the disk of this machine", func(t *testing.T) {
		if _, err := os.Stat("/proc/loadavg"); err != nil {
			t.Skip("needs Linux")
		}
		t.Setenv("CHASEN_ROOT", t.TempDir())
		read, write, _ := os.Pipe()
		stdout := os.Stdout
		os.Stdout = write

		err := serverLoad()

		os.Stdout = stdout
		write.Close()
		out, _ := io.ReadAll(read)
		if err != nil {
			t.Fatal(err)
		}
		want := regexp.MustCompile(`^Load:     [0-9.]+ [0-9.]+ [0-9.]+ \(\d+ cores, (amd64|arm64)\)\nMemory:   [0-9.]+ GB of [0-9.]+ GB \(\d+%\)\nDisk:     [0-9.]+ GB of [0-9.]+ GB \(\d+%\)\n$`)
		if !want.Match(out) {
			t.Errorf("serverLoad printed:\n%s", out)
		}
	})

	t.Run("says how much is in use, with the percent", func(t *testing.T) {
		if got := inUse(3<<30, 12<<30); got != "3.0 GB of 12 GB (25%)" {
			t.Errorf("inUse = %q", got)
		}
		if got := memoryKB("MemTotal:       16303428 kB\nMemAvailable:    9128312 kB\n", "MemAvailable"); got != 9128312 {
			t.Errorf("memoryKB = %d", got)
		}
	})
}
