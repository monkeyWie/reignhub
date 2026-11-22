// Package deviceservice provides the account-free device directory, pairing mailbox and relay.
package deviceservice

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/monkeyWie/reignhub/pkg/access"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/device"

	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Device struct {
	ID           string `json:"id" gorm:"primaryKey;size:64"`
	ConnectionID string `json:"connectionId" gorm:"uniqueIndex;size:32"`
	Name         string `json:"name"`
	Public       string `json:"public"`
	TokenHash    string `json:"tokenHash,omitempty" gorm:"uniqueIndex;size:64"`
	Host         bool   `json:"host"`
	Seen         int64  `json:"seen"`
}
type Pair struct {
	ID       string `json:"id" gorm:"primaryKey;size:32"`
	Host     string `json:"host" gorm:"index:pair_pending"`
	Client   string `json:"client"`
	Name     string `json:"name"`
	Public   string `json:"public"`
	Envelope string `json:"envelope"`
	Reply    string `json:"reply,omitempty"`
	State    string `json:"state" gorm:"index:pair_pending"`
	Expires  int64  `json:"expires" gorm:"index"`
}
type DeviceGrant struct {
	Pairing string
	Host    string `gorm:"primaryKey;size:64"`
	Client  string `gorm:"primaryKey;size:64"`
}
type Metadata struct {
	Key   string `gorm:"primaryKey;size:64"`
	Value string
}
type bucket struct {
	At time.Time
	N  int
}
type Admission struct {
	Operation string
	DeviceID  string
	HostID    string
}
type Server struct {
	// Hooks are assigned before serving. Registration runs in the same database
	// transaction, so a rejected allocation cannot leave an unowned device.
	OnRegister     func(*gorm.DB, Device) error
	Policy         func(context.Context, Admission) error
	mu             sync.Mutex
	DB             *gorm.DB
	rates          map[string]bucket
	nextCleanup    int64
	relay          *relay.Server
	signer         ed25519.PrivateKey
	nativeKey      ed25519.PrivateKey
	ServiceID      string
	Network        connectivity.Policy
	STUNPort       string
	TCPSTUNPort    string
	ConfiguredSTUN connectivity.Endpoints
}

func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func New(path string, origins []string) (*Server, error) {
	db, err := database.OpenSQLite(path)
	if err != nil {
		return nil, err
	}
	s, err := NewWithDB(db, origins)
	if err != nil {
		raw, _ := db.DB()
		_ = raw.Close()
	}
	return s, err
}

// NewWithDB composes the directory with any configured GORM database. The
// caller owns the database lifetime. SQLite remains the self-hosted default.
func NewWithDB(db *gorm.DB, origins []string) (*Server, error) {
	if err := db.AutoMigrate(&Device{}, &DeviceGrant{}, &Metadata{}, &Pair{}); err != nil {
		return nil, err
	}
	meta := Metadata{Key: "service_id", Value: device.Random()}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&meta).Error; err != nil {
		return nil, err
	}
	if err := db.First(&meta, "key = ?", "service_id").Error; err != nil {
		return nil, err
	}
	s := &Server{DB: db, rates: map[string]bucket{}, Network: connectivity.DefaultPolicy(), ServiceID: meta.Value}
	s.relay = relay.New("")
	s.relay.Origins = origins
	// Persist the signing seed with the service ID; restarting must not invalidate leases.
	_, generated, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	key := Metadata{Key: signingSeedKey, Value: base64.RawURLEncoding.EncodeToString(generated.Seed())}
	if err = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&key).Error; err != nil {
		return nil, err
	}
	seed, err := readSigningSeed(db)
	if err != nil {
		return nil, err
	}
	s.nativeKey = relayTLSKey(seed)
	private := ed25519.NewKeyFromSeed(seed)
	s.signer = private
	public := private.Public().(ed25519.PublicKey)
	s.Network.ServiceID = s.ServiceID
	s.Network.GrantPublicKey = base64.RawURLEncoding.EncodeToString(public)
	s.Network.Deployment = "self-hosted"
	s.relay.Grant = func(host, client, role string) string {
		now := time.Now().Unix()
		ticket, _ := access.Sign(private, access.Grant{Audience: access.Audience, Namespace: s.ServiceID, HostID: host, DeviceID: client, Role: role, Scopes: []string{"connect", "relay"}, IssuedAt: now, ExpiresAt: now + 180, ID: relay.ID()})
		return ticket
	}
	s.relay.AuthorizeContext = func(ctx context.Context, token, host, role string) (string, bool) {
		id := s.identity(token)
		if id == "" {
			return "", false
		}
		var target Device
		if s.DB.First(&target, "id = ? AND host = ?", host, true).Error != nil {
			return "", false
		}
		if s.Policy != nil && s.Policy(ctx, Admission{Operation: "relay." + role, DeviceID: id, HostID: host}) != nil {
			return "", false
		}
		if role == "host" {
			return id, id == host
		}
		var grant DeviceGrant
		return id, role == "client" && s.DB.First(&grant, "host = ? AND client = ?", host, id).Error == nil
	}
	return s, nil
}
func (s *Server) ConfigureNative(nodes []relay.RelayNode, websocket bool) {
	s.relay.DisableWebSocket = !websocket
	s.relay.ExternalRelays = nodes
}
func (s *Server) SetRevokedRelayDevices(ids []string) {
	s.relay.SetRevokedDevices(ids)
}
func (s *Server) ServeNode(w http.ResponseWriter, r *http.Request) {
	s.relay.ServeNode(w, r, s.nativeKey)
}

func (s *Server) ListenNative(address, public string) (*relay.NativeListener, error) {
	return s.relay.ListenNative(address, public, s.nativeKey)
}
func (s *Server) Close() error {
	s.relay.Close()
	raw, err := s.DB.DB()
	if err != nil {
		return err
	}
	return raw.Close()
}
func (s *Server) identity(token string) string {
	if token == "" {
		return ""
	}
	var d Device
	if s.DB.First(&d, "token_hash = ?", hash(token)).Error != nil {
		return ""
	}
	return d.ID
}
func (s *Server) allow(k string, limit int) bool {
	now := time.Now()
	if len(s.rates) > 20000 {
		for key, v := range s.rates {
			if now.Sub(v.At) > time.Minute {
				delete(s.rates, key)
			}
		}
		if _, ok := s.rates[k]; !ok && len(s.rates) > 20000 {
			return false
		}
	}
	b := s.rates[k]
	if now.Sub(b.At) > time.Minute {
		b = bucket{At: now}
	}
	b.N++
	s.rates[k] = b
	return b.N <= limit
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	origin := r.Header.Get("Origin")
	if origin != "" {
		allowed := origin == "http://"+r.Host || origin == "https://"+r.Host
		for _, o := range s.relay.Origins {
			if o == origin {
				allowed = true
			}
		}
		if !allowed {
			apiError(w, "origin denied", 403)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	}
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/p2p/signal" || r.URL.Path == "/p2p/relay" {
		s.relay.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/p2p/config" && (r.Method == "GET" || r.Method == "POST") {
		policy := s.Network
		if len(policy.SelfSTUN) == 0 {
			if endpoint := connectivity.STUNURL(r.Host, s.STUNPort); endpoint != "" {
				policy.SelfSTUN = []string{endpoint}
			}
		}
		if len(policy.TCPSTUN) == 0 && s.TCPSTUNPort != "" {
			policy.TCPSTUN = []string{strings.TrimPrefix(connectivity.STUNURL(r.Host, s.TCPSTUNPort), "stun:")}
		}
		s.ConfiguredSTUN.AppendSTUN(&policy)
		writeJSON(w, 200, struct {
			connectivity.Policy
			Protocol        int      `json:"protocol"`
			AccountRequired bool     `json:"accountRequired"`
			LoginProviders  []string `json:"loginProviders"`
		}{policy, 1, false, []string{}})
		return
	}
	if r.Method != "POST" {
		apiError(w, "POST required", 405)
		return
	}
	var in struct {
		Name, Public, ConnectionID, Host, ID, Envelope, Reply, Client, Pairing string
		Hosts                                                                  []string
		IsHost                                                                 bool
		Allow                                                                  bool
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); e != nil {
		apiError(w, "invalid request", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.allow("ip:"+ip, 300) {
		apiError(w, "rate limited", 429)
		return
	}
	now := time.Now().Unix()
	if now >= s.nextCleanup {
		if err := s.DB.Where("expires <= ?", now).Delete(&Pair{}).Error; err != nil {
			apiError(w, "database unavailable", 503)
			return
		}
		s.nextCleanup = now + 60
	}
	write := func(v any) { writeJSON(w, 200, v) }
	if r.URL.Path == "/p2p/devices/register" {
		var count int64
		if s.DB.Model(&Device{}).Count(&count).Error != nil {
			apiError(w, "database unavailable", 503)
			return
		}
		if count >= 10000 || !s.allow("register:"+ip, 10) {
			apiError(w, "registration limit", 429)
			return
		}
		raw, e := hex.DecodeString(in.Public)
		if e != nil || len(raw) != 32 || strings.ToLower(in.Public) != in.Public || len(in.Name) > 100 {
			apiError(w, "invalid identity", 400)
			return
		}
		token := device.Random()
		id := relay.ID()
		var code string
		for {
			n, e := rand.Int(rand.Reader, big.NewInt(1_000_000))
			if e != nil {
				apiError(w, "device ID allocation failed", 503)
				return
			}
			code = fmt.Sprintf("%06d", n.Int64())
			var count int64
			if s.DB.Model(&Device{}).Where("connection_id = ?", code).Count(&count).Error != nil {
				apiError(w, "database unavailable", 503)
				return
			}
			if count == 0 {
				break
			}
		}
		d := Device{ID: id, ConnectionID: code, Name: in.Name, Public: in.Public, TokenHash: hash(token), Host: in.IsHost, Seen: now}
		if e = s.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&d).Error; err != nil {
				return err
			}
			if s.OnRegister != nil {
				return s.OnRegister(tx, d)
			}
			return nil
		}); e != nil {
			apiError(w, "device registration failed", 503)
			return
		}

		write(map[string]any{"id": id, "connectionId": code, "token": token, "serviceId": s.ServiceID})
		return
	}
	id := s.identity(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if id == "" {
		apiError(w, "unauthorized", 401)
		return
	}
	var me Device
	if s.DB.First(&me, "id = ?", id).Error != nil {
		apiError(w, "database unavailable", 503)
		return
	}
	if s.Policy != nil {
		if err := s.Policy(r.Context(), Admission{Operation: r.URL.Path, DeviceID: id, HostID: in.Host}); err != nil {
			apiError(w, err.Error(), http.StatusForbidden)
			return
		}
	}
	switch r.URL.Path {
	case "/p2p/routes":
		if (r.Header.Get("Origin") != "" && id == in.Host) || !validID(in.Host) || !s.CanAccess(id, in.Host) {
			apiError(w, "permission denied", 403)
			return
		}
		role := "client"
		if id == in.Host {
			role = "host"
		}
		ticket, err := access.Sign(s.signer, access.Grant{Audience: access.Audience, Namespace: s.ServiceID, HostID: in.Host, DeviceID: id, Role: role, Scopes: []string{"connect", "offline"}, IssuedAt: now, ID: relay.ID()})
		if err != nil {
			apiError(w, "signing unavailable", 503)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		write(relay.Routes{Grant: ticket, Node: s.relay.Enroll(s.ServiceID, in.Host, id, role, r.Host, s.nativeKey)})
	case "/p2p/devices/presence":
		name := me.Name
		if in.Name != "" && len(in.Name) <= 100 {
			name = in.Name
		}
		if now-me.Seen >= 300 || name != me.Name {
			me.Name, me.Seen = name, now
			if e := s.DB.Save(&me).Error; e != nil {
				apiError(w, "persistence failed", 500)
				return
			}
		}
		write(map[string]bool{"ok": true})
	case "/p2p/devices/status":
		if len(in.Hosts) > 50 {
			apiError(w, "at most 50 hosts", http.StatusBadRequest)
			return
		}
		for _, host := range in.Hosts {
			if !validID(host) {
				apiError(w, "invalid host", http.StatusBadRequest)
				return
			}
		}
		var grants []DeviceGrant
		if err := s.DB.Where("client = ? AND host IN ?", id, in.Hosts).Find(&grants).Error; err != nil {
			apiError(w, "presence unavailable", 503)
			return
		}
		allowed := map[string]bool{id: true}
		for _, g := range grants {
			allowed[g.Host] = true
		}
		var hosts []Device
		if err := s.DB.Where("id IN ? AND host = ?", in.Hosts, true).Find(&hosts).Error; err != nil {
			apiError(w, "presence unavailable", 503)
			return
		}
		visible := map[string]Device{}
		for _, h := range hosts {
			if allowed[h.ID] {
				visible[h.ID] = h
			}
		}
		out := []map[string]any{}
		for _, host := range in.Hosts {
			item := map[string]any{"host": host, "online": nil}
			if d, ok := visible[host]; ok {
				item["name"], item["online"], item["checkedAt"] = d.Name, s.relay.Online(host), now
			}
			out = append(out, item)
		}
		write(map[string]any{"devices": out})
	case "/p2p/devices/resolve":
		code := strings.ReplaceAll(strings.ReplaceAll(in.ConnectionID, " ", ""), "-", "")
		var d Device
		if s.DB.First(&d, "connection_id = ? AND host = ?", code, true).Error != nil {
			apiError(w, "device not found", 404)
			return
		}
		d.TokenHash = ""

		write(map[string]any{"device": d, "online": s.relay.Online(d.ID)})

	case "/p2p/pairings/request":
		var host Device
		err := s.DB.First(&host, "id = ?", in.Host).Error
		if err != nil || !host.Host || len(in.Envelope) > 32000 || !validID(in.Host) || !validID(in.ID) {
			apiError(w, "invalid pairing target", 400)
			return
		}
		var count int64
		if err := s.DB.Model(&Pair{}).Count(&count).Error; err != nil {
			apiError(w, "database unavailable", 503)
			return
		}
		if !s.allow("pair:"+id, 10) || !s.allow("hostpair:"+host.ID, 30) || count > 1000 {
			apiError(w, "pairing rate limited", 429)
			return
		}
		var existing Pair
		if s.DB.First(&existing, "id = ?", in.ID).Error == nil {
			apiError(w, "duplicate request", 409)
			return
		}
		p := Pair{ID: in.ID, Host: host.ID, Client: id, Name: me.Name, Public: me.Public, Envelope: in.Envelope, State: "pending", Expires: now + 120}
		if err := s.DB.Create(&p).Error; err != nil {
			apiError(w, "persistence failed", 503)
			return
		}
		write(p)
	case "/p2p/pairings/pending":
		out := []Pair{}
		if err := s.DB.Where("host = ? AND state = ? AND expires > ?", id, "pending", now).Order("expires, id").Find(&out).Error; err != nil {
			apiError(w, "database unavailable", 503)
			return
		}
		write(out)
	case "/p2p/pairings/result":
		var p Pair
		if s.DB.First(&p, "id = ? AND client = ? AND expires > ?", in.ID, id, now).Error != nil {
			apiError(w, "pairing expired", 404)
			return
		}
		write(p)
	case "/p2p/pairings/respond":
		var p Pair
		if s.DB.First(&p, "id = ? AND host = ? AND state = ? AND expires > ?", in.ID, id, "pending", now).Error != nil {
			apiError(w, "request unavailable", 409)
			return
		}
		p.State = "denied"
		if in.Allow {
			if in.Reply == "" || len(in.Reply) > 32000 {
				apiError(w, "missing encrypted reply", 400)
				return
			}

			p.State = "approved"
			p.Reply = in.Reply
		}
		if err := s.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Save(&p).Error; err != nil {
				return err
			}
			if p.State == "approved" {
				return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "host"}, {Name: "client"}}, DoUpdates: clause.AssignmentColumns([]string{"pairing"})}).Create(&DeviceGrant{Host: p.Host, Client: p.Client, Pairing: p.ID}).Error
			}
			return nil
		}); err != nil {
			apiError(w, "persistence failed", 503)
			return
		}
		write(p)
	case "/p2p/pairings/revoke":
		q := s.DB.Where("host = ? AND client = ?", id, in.Client)
		if in.Pairing != "" {
			q = q.Where("pairing = ?", in.Pairing)
		}
		revoked := q.Delete(&DeviceGrant{})
		if revoked.Error != nil {
			apiError(w, "persistence failed", 500)
			return
		}
		if revoked.RowsAffected > 0 {
			s.relay.DisconnectDevice(id, in.Client)
		}
		write(map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

// Authenticate resolves a device credential without exposing its stored token hash.
func (s *Server) Authenticate(token string) (Device, bool) {
	id := s.identity(token)
	var d Device
	if id == "" || s.DB.First(&d, "id = ?", id).Error != nil {
		return d, false
	}
	d.TokenHash = ""
	return d, true
}

// CanAccess checks core device authorization independently of cloud identity.
func (s *Server) CanAccess(deviceID, hostID string) bool {
	var host Device
	if s.DB.First(&host, "id = ? AND host = ?", hostID, true).Error != nil {
		return false
	}
	if deviceID == hostID {
		return true
	}
	var grant DeviceGrant
	return s.DB.First(&grant, "host = ? AND client = ?", hostID, deviceID).Error == nil
}

func validID(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16 && strings.ToLower(s) == s
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, message string, status int) {
	code := map[int]string{400: "INVALID_REQUEST", 401: "DEVICE_REQUIRED", 403: "ORIGIN_DENIED", 404: "NOT_FOUND", 405: "METHOD_NOT_ALLOWED", 409: "PAIRING_EXPIRED", 429: "RATE_LIMITED", 500: "SERVICE_UNAVAILABLE", 503: "SERVICE_UNAVAILABLE"}[status]
	if message == "pairing expired" {
		code = "PAIRING_EXPIRED"
	}
	if message == "device not found" {
		code = "DEVICE_NOT_FOUND"
	}
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
