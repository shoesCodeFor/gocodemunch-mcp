//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly && !solaris

package main

type cpuUsageSnapshot struct {
	UserSeconds   float64
	SystemSeconds float64
}

func readCPUUsageSnapshot() (cpuUsageSnapshot, error) {
	return cpuUsageSnapshot{}, nil
}
