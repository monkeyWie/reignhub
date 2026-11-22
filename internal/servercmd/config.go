package servercmd

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/relay"
)

type commandAction int

const (
	serve commandAction = iota
	initConfig
	printVersion
	printRelayCredential
)

type command struct {
	file   string
	action commandAction
}

func parseCommand(args []string, output io.Writer) (command, error) {
	var cmd command
	f := flag.NewFlagSet("reignhub-server", flag.ContinueOnError)
	f.SetOutput(output)
	f.StringVar(&cmd.file, "config", "config.json", "path to the service configuration")
	init := f.Bool("init", false, "create a default config file and exit")
	credential := f.Bool("print-relay-credential", false, "print the secret relay registration credential from the configured state and exit")
	version := f.Bool("version", false, "print the version and exit")
	if err := f.Parse(args); err != nil {
		return cmd, err
	}
	if f.NArg() != 0 || cmd.file == "" {
		return cmd, errors.New("expected --config PATH or a management action (see --help)")
	}
	for _, opt := range []struct {
		enabled bool
		action  commandAction
	}{{*init, initConfig}, {*credential, printRelayCredential}, {*version, printVersion}} {
		if opt.enabled {
			if cmd.action != serve {
				return cmd, errors.New("choose one of --init, --print-relay-credential or --version")
			}
			cmd.action = opt.action
		}
	}
	return cmd, nil
}

func writeDefaultConfig(path string) error {
	data, err := json.MarshalIndent(configFile{Services: allServices()}, "", "  ")
	if err != nil {
		return err
	}
	// Publish a complete file exclusively, including when two initializers race.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".reignhub-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(data, '\n'))
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("config %q already exists", path)
		}
		return err
	}
	return nil
}

func loadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, fmt.Errorf("config %q is missing; use --init to create it: %w", path, err)
		}
		return c, err
	}
	defer f.Close()
	const maxSize = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return c, err
	}
	if len(data) > maxSize {
		return c, fmt.Errorf("config %q exceeds 1 MiB", path)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return c, fmt.Errorf("config %q must contain one JSON object", path)
	}
	// A null service or address list can otherwise silently select defaults.
	tokens := json.NewDecoder(bytes.NewReader(data))
	for {
		token, err := tokens.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return c, fmt.Errorf("config %q: %w", path, err)
		}
		if token == nil {
			return c, fmt.Errorf("config %q: null is unsupported", path)
		}
	}
	file := configFile{Services: allServices()}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&file); err != nil {
		return c, fmt.Errorf("config %q: %w", path, err)
	}
	if d.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("config %q must contain one JSON object", path)
	}
	c, err = file.Services.resolve()
	if err != nil {
		return c, fmt.Errorf("config %q: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return c, err
	}
	for _, value := range []*string{&c.State, &c.Web, &c.TLSCert, &c.TLSKey} {
		if *value != "" && !filepath.IsAbs(*value) {
			*value = filepath.Join(filepath.Dir(abs), *value)
		}
	}
	return c, nil
}

type configFile struct {
	Services servicesConfig `json:"services"`
}

type servicesConfig struct {
	Control controlConfig `json:"control,omitempty"`
	STUN    stunConfig    `json:"stun,omitempty"`
	Relay   relayConfig   `json:"relay,omitempty"`
}

type controlConfig struct {
	Enable         bool                            `json:"enable"`
	Listen         string                          `json:"listen,omitempty"`
	Database       string                          `json:"database,omitempty"`
	WebDir         string                          `json:"webDir,omitempty"`
	AllowedOrigins []string                        `json:"allowedOrigins,omitempty"`
	STUNServers    connectivity.TransportAddresses `json:"stunServers,omitempty"`
	ExternalRelays []relay.NodeConfig              `json:"externalRelays,omitempty"`
	TLS            *struct {
		CertFile string `json:"certFile"`
		KeyFile  string `json:"keyFile"`
	} `json:"tls,omitempty"`
}

type listenerConfig struct {
	Listen        string `json:"listen,omitempty"`
	PublicAddress string `json:"publicAddress,omitempty"`
}

type transportConfig struct {
	listenerConfig
	Enable *bool `json:"enable,omitempty"`
}

func (t *transportConfig) addresses(listen, public string) (string, string) {
	if t == nil {
		return listen, public
	}
	if t.Enable != nil && !*t.Enable {
		return "", ""
	}
	return cmp.Or(t.Listen, listen), cmp.Or(t.PublicAddress, public)
}

type stunConfig struct {
	Enable bool `json:"enable"`
	listenerConfig
	UDP *transportConfig `json:"udp,omitempty"`
	TCP *transportConfig `json:"tcp,omitempty"`
}

type relayConfig struct {
	RevokedDevices []string         `json:"revokedDevices,omitempty"`
	Enable         bool             `json:"enable"`
	WebSocket      *bool            `json:"websocket,omitempty"`
	Native         *transportConfig `json:"native,omitempty"`
	Database       string           `json:"database,omitempty"`
}

func allServices() servicesConfig {
	return servicesConfig{Control: controlConfig{Enable: true}, STUN: stunConfig{Enable: true}, Relay: relayConfig{Enable: true}}
}

// Config contains the resolved startup settings. An empty listener address disables that listener.
type Config struct {
	RevokedDevices                                       []string
	Control, WebSocket                                   bool
	Listen, State, Web, TLSCert, TLSKey                  string
	Origins                                              []string
	STUNServers                                          connectivity.Endpoints
	STUNListen, TCPSTUNListen, STUNPublic, TCPSTUNPublic string
	NativeListen, NativePublic                           string
	Nodes                                                []relay.RelayNode
}

func (s servicesConfig) resolve() (Config, error) {
	c := Config{State: "data/reignhub.db"}
	if ctrl := s.Control; ctrl.Enable || s.Relay.Enable && (s.Relay.WebSocket == nil || *s.Relay.WebSocket) {
		c.Control = ctrl.Enable
		c.Listen = cmp.Or(ctrl.Listen, ":26330")
		c.State, c.Web = cmp.Or(ctrl.Database, c.State), ctrl.WebDir
		c.Origins = ctrl.AllowedOrigins
		for _, origin := range c.Origins {
			if !validOrigin(origin) {
				return c, fmt.Errorf("control.allowedOrigins: invalid origin %q", origin)
			}
		}
		if ctrl.TLS != nil {
			c.TLSCert, c.TLSKey = ctrl.TLS.CertFile, ctrl.TLS.KeyFile
			if c.TLSCert == "" || c.TLSKey == "" {
				return c, errors.New("control.tls requires both certFile and keyFile")
			}
		}
		var err error
		c.STUNServers, err = ctrl.STUNServers.Resolve()
		if err != nil {
			return c, fmt.Errorf("control.stunServers: %w", err)
		}
		c.Nodes, err = relay.ResolveNodes(ctrl.ExternalRelays)
		if err != nil {
			return c, fmt.Errorf("control.externalRelays: %w", err)
		}
	}
	if st := s.STUN; st.Enable {
		shared := cmp.Or(st.Listen, ":"+connectivity.DefaultSTUNPort)
		c.STUNListen, c.STUNPublic = st.UDP.addresses(shared, st.PublicAddress)
		c.TCPSTUNListen, c.TCPSTUNPublic = st.TCP.addresses(shared, st.PublicAddress)
	}
	if r := s.Relay; r.Enable {
		for _, id := range r.RevokedDevices {
			if _, err := hex.DecodeString(id); len(id) != 32 || id != strings.ToLower(id) || err != nil {
				return c, errors.New("relay.revokedDevices requires lowercase 32-character hex device IDs")
			}
		}
		c.RevokedDevices = r.RevokedDevices
		if c.Control && r.Database != "" {
			return c, errors.New("local relay uses control.database")
		}
		if !c.Control {
			c.State = cmp.Or(r.Database, c.State)
		}
		c.WebSocket = true
		if r.WebSocket != nil {
			c.WebSocket = *r.WebSocket
		}
		c.NativeListen, c.NativePublic = r.Native.addresses(":26332", "")
	}
	if !c.Control && !c.WebSocket && c.STUNListen == "" && c.TCPSTUNListen == "" && c.NativeListen == "" {
		return c, errors.New("enable at least one service listener")
	}
	for _, addr := range []string{c.STUNPublic, c.TCPSTUNPublic, c.NativePublic} {
		if addr != "" {
			// Prefix is fixed here: publicAddress is a single host:port.
			if _, err := (connectivity.TransportAddresses{"udp:" + addr}).Resolve(); err != nil {
				return c, fmt.Errorf("invalid publicAddress %q: expected host:port", addr)
			}
		}
	}
	return c, nil
}
