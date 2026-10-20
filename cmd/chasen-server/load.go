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
// /var/matcha is the disk of the host. Then the two ways out of the server
// that the map of the screen draws: the bucket of the backups, and where the
// API answers.
func serverLoad() error {
	load, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return errors.New("this machine has no /proc/loadavg: chasen-server runs on Linux")
	}
	averages := strings.Fields(string(load))
	if len(averages) < 3 {
		return fmt.Errorf("/proc/loadavg has an unknown format: %q", load)
	}
	// The CPU type too: the CLI builds the image of an app for it.
	fmt.Printf("Load:     %s (%d cores, %s)\n", strings.Join(averages[:3], " "), runtime.NumCPU(), runtime.GOARCH)

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

	cfg, err := loadServerConfig()
	if err != nil {
		return nil // the numbers are what the load is for
	}
	fmt.Printf("Bucket:   %s\n", bucketLine(cfg))
	fmt.Printf("API:      %s\n", apiLine(cfg))
	return nil
}

func bucketLine(cfg serverConfig) string {
	if cfg.Backup.S3 == nil {
		return "none: the backups stay on this server"
	}
	return cfg.Backup.S3.Bucket
}

// apiLine says where the API answers, by the rule of the api alert.
func apiLine(cfg serverConfig) string {
	switch {
	case cfg.Domain == "":
		return "SSH only: not on the web"
	case isLocal(cfg.Domain):
		return "https://api." + cfg.Domain + ", on this machine"
	}
	return "https://api." + cfg.Domain + ", on the internet"
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
