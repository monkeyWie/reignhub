//go:build (darwin && !ios) || windows

package main

import "github.com/monkeyWie/reignhub/internal/codex"

func runSharedWorker() bool { return codex.RunSharedWorker() }
