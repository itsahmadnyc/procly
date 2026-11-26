//go:build !linux && !darwin

package sysinfo

func read(*System) {}
