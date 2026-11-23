//go:build (darwin && !ios) || windows || (linux && !android)

// Package terminalengine adapts NSGo's official terminal to the host boundary.
package terminalengine

import (
	"context"
	"sync"

	model "github.com/monkeyWie/reignhub/internal/terminal"
	"github.com/nsgo-dev/nsgo/base/terminal"
)

type engine struct {
	t        *terminal.Terminal
	m        *terminal.Mirror
	once     sync.Once
	closeErr error
}

func New(ctx context.Context, dir string, size model.Size, shellPath string) (model.Engine, error) {
	opts := terminal.Options{Dir: dir, Scrollback: 512 << 10}
	if shellPath != "" {
		opts.Command = []string{shellPath}
	}
	return newEngine(ctx, opts, size)
}
func newEngine(ctx context.Context, options terminal.Options, size model.Size) (model.Engine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t, err := terminal.New(options)
	if err != nil {
		return nil, err
	}
	m, err := t.NewMirror()
	if err != nil {
		_ = t.Close()
		return nil, err
	}
	e := &engine{t: t, m: m}
	e.Resize(size)
	if err := ctx.Err(); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (e *engine) Resize(s model.Size) { e.t.ResizePixels(s.Cols, s.Rows, s.CellWidth, s.CellHeight) }
func (e *engine) Frame(reset bool) []byte {
	if reset {
		e.m.Reset()
	}
	b, _ := e.m.Frame()
	return b
}
func (e *engine) Send(b []byte)         { e.t.Send(b) }
func (e *engine) Done() <-chan struct{} { return e.t.Done() }
func (e *engine) ExitCode() int         { return e.t.ExitCode() }
func (e *engine) Close() error {
	e.once.Do(func() { e.m.Close(); e.closeErr = e.t.Close() })
	return e.closeErr
}
