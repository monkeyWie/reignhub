package hubui

import "github.com/nsgo-dev/nsgo/core"

// Panel preferences survive navigation; host processes have a separate lifetime.
type terminalPanel struct {
	visible    bool
	height     core.Points
	tabs       []*terminalTab
	active     *terminalTab
	nextNumber int
}

type terminalTab struct {
	profile, profileName string
	id, title            string
	reportedPath         string
	reportedShell        string
	number, revision     int
	openDone             chan struct{}
	closing              bool
	closeError           string
	receiver             *terminalReceiver
	inputUnconfirmed     bool
}

func (s *State) terminalVisible() bool {
	p := s.terminalPanels[s.key()]
	return p != nil && p.visible
}
