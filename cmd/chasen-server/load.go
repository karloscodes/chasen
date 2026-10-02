package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// serverLoad prints how busy the server is: the load, the memory in use, and
// the disk that holds the apps. The API runs in a container, but these are
// the numbers of the whole machine: /proc is the one of the kernel, and
// /var/matcha is the disk of the host.
func serverLoad() error {
	load, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return errors.New("this machine has no /proc/loadavg: chasen-server runs on Linux")
	}
	averages := strings.Fields(string(load))
	if len(averages) < 3 {
		return fmt.Errorf("/proc/loadavg has an unknown format: %q", load)
	}
	fmt.Printf("Load:     %s (%d cores)\n", strings.Join(averages[:3], " "), runtime.NumCPU())

	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return err
	}
	total, available := memoryKB(string(meminfo), "MemTotal"), memoryKB(string(meminfo), "MemAvailable")
	fmt.Printf("Memory:   %s\n", inUse((total-available)*1024, total*1024))

	dir := root() + "/var/matcha"
	if _, err := os.Stat(dir); err != nil {
		dir = "/"
	}
	var disk syscall.Statfs_t
	if err := syscall.Statfs(dir, &disk); err != nil {
		return err
	}
	block := uint64(disk.Bsize)
	fmt.Printf("Disk:     %s\n", inUse((disk.Blocks-disk.Bfree)*block, disk.Blocks*block))
	return nil
}

// memoryKB returns one value of /proc/meminfo, in kB: "MemTotal:  16303428 kB".
func memoryKB(meminfo, name string) uint64 {
	for _, line := range strings.Split(meminfo, "\n") {
		if rest, ok := strings.CutPrefix(line, name+":"); ok {
			if fields := strings.Fields(rest); len(fields) > 0 {
				kb, _ := strconv.ParseUint(fields[0], 10, 64)
				return kb
			}
		}
	}
	return 0
}

// inUse says how much of something is in use: "1.2 GB of 3.8 GB (31%)".
func inUse(used, total uint64) string {
	percent := 0
	if total > 0 {
		percent = int(used * 100 / total)
	}
	return fmt.Sprintf("%s of %s (%d%%)", gigabytes(used), gigabytes(total), percent)
}

func gigabytes(bytes uint64) string {
	gb := float64(bytes) / (1 << 30)
	if gb >= 10 {
		return fmt.Sprintf("%.0f GB", gb)
	}
	return fmt.Sprintf("%.1f GB", gb)
}
