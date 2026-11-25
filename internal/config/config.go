package config

import (
	"crypto/rand"

	"encoding/hex"

	"fmt"

	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	UIPath     string `json:"uiPath,omitempty"`
	ServiceURL string `json:"serviceUrl,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
	// AgentEnabled is a local integration preference. Missing entries default on.
	AgentEnabled          map[string]bool  `json:"agentEnabled,omitempty"`
	Agents                map[string]Agent `json:"agents,omitempty"`
	DataDir               string           `json:"-"`
	Listen                string           `json:"listen"`
	RemoteListen          string           `json:"remoteListen"`
	RemoteEnabled         bool             `json:"remoteEnabled"`
	RemoteTerminalEnabled bool             `json:"remoteTerminalEnabled"`
	DesktopListen         string           `json:"desktopListen"`
	CodexCLI              string           `json:"codexCli"`
	CodexApp              string           `json:"codexApp"`
	TLSCert               string           `json:"tlsCert,omitempty"`
	TLSKey                string           `json:"tlsKey,omitempty"`
	RelayURL              string           `json:"relayUrl,omitempty"`
	RelayToken            string           `json:"relayToken,omitempty"`
	HostID                string           `json:"hostId,omitempty"`
	STUN                  []string         `json:"stun,omitempty"`
}

func (c Config) AgentIsEnabled(id string) bool {
	if enabled, ok := c.AgentEnabled[id]; ok {
		return enabled
	}
	return !c.Agents[id].Disabled
}

// Agent launch settings are local configuration. Remote peers cannot supply
// executables, shell commands, or environment overrides through the RPC API.
type Agent struct {
	Name     string            `json:"name,omitempty"`
	Protocol string            `json:"protocol"`
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

func DefaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agenthub")
}

func Load(dir string) (Config, error) {
	c := Config{DataDir: dir, RemoteTerminalEnabled: true, Listen: "127.0.0.1:7331", RemoteListen: "0.0.0.0:7332", DesktopListen: "127.0.0.1:7333", STUN: []string{"stun:stun.cloudflare.com:3478", "stun:stun.l.google.com:19302"}}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return c, err
	}
	if err := readSettings(dir, &c); err != nil {
		return c, err
	}

	c.DataDir = dir
	return c, nil
}

var saveMu sync.Mutex

func (c Config) Save() error {
	saveMu.Lock()
	defer saveMu.Unlock()
	return writeSettings(c)
}

func AccessKey(dir string) (string, error) {
	p := filepath.Join(dir, "access-key")
	read := func() (string, error) {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		decoded, err := hex.DecodeString(string(b))
		if err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("invalid access key in %s", p)
		}
		return string(b), nil
	}
	if key, err := read(); err == nil {
		return key, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b)
	// Publish only the complete file, including when multiple processes start together.
	f, err := os.CreateTemp(dir, ".access-key-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(key); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Link(f.Name(), p); os.IsExist(err) {
		return read()
	} else if err != nil {
		return "", err
	}
	return key, nil
}
