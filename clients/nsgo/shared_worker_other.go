//go:build (!darwin && !windows) || ios

package main

func runSharedWorker() bool { return false }
