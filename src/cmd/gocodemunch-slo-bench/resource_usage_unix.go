//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package main

import "syscall"

type cpuUsageSnapshot struct {
	UserSeconds   float64
	SystemSeconds float64
}

func readCPUUsageSnapshot() (cpuUsageSnapshot, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return cpuUsageSnapshot{}, err
	}

	return cpuUsageSnapshot{
		UserSeconds:   timevalToSeconds(usage.Utime),
		SystemSeconds: timevalToSeconds(usage.Stime),
	}, nil
}

func timevalToSeconds(tv syscall.Timeval) float64 {
	return float64(tv.Sec) + float64(tv.Usec)/1_000_000
}
