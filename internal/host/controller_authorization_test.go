package host

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/pkg/appdb"
)

func TestControllerApprovalOrderAndPersistence(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/p2p/pairings/respond" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer service.Close()
	identity, err := device.NewIdentity("host fixture")
	if err != nil {
		t.Fatal(err)
	}
	identity.Service = service.URL
	controller, err := device.NewIdentity("controller fixture")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	request := deviceservice.Pair{ID: "new", Client: "c", Name: controller.Name, Public: controller.Public, Expires: at.Add(time.Minute).Unix()}
	m := &Manager{
		path:    appdb.Path(t.TempDir()),
		pending: map[string]deviceservice.Pair{request.ID: request},
		intents: map[string]Intent{request.ID: {}},
		state: saved{Identity: identity, Grants: map[string]Grant{
			"a": {Client: "a", Name: "Legacy grant"},
			"b": {Client: "b", Name: "Earlier approval", AuthorizedAt: at.Add(-time.Hour).UnixMilli()},
		}},
	}
	approve := func() {
		t.Helper()
		params, err := json.Marshal(map[string]any{"id": request.ID, "allow": true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Local(t.Context(), control.Request{Method: "host.respond", Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	approve()
	stamp := m.state.Grants["c"].AuthorizedAt
	if stamp < at.UnixMilli() || stamp > time.Now().UnixMilli() {
		t.Fatalf("grant did not record its approval time: %d", stamp)
	}
	// Reprocessing the same approval or updating the profile preserves its date.
	m.pending[request.ID] = request
	m.intents[request.ID] = Intent{}
	approve()
	if err := m.updateClientInfo("c", device.ClientInfo{Name: "Renamed fixture"}); err != nil {
		t.Fatal(err)
	}
	info, err := m.Local(t.Context(), control.Request{Method: "host.info"})
	if err != nil {
		t.Fatal(err)
	}
	grants := info.(map[string]any)["grants"].([]map[string]any)
	if len(grants) != 3 || grants[0]["client"] != "c" || grants[0]["authorizedAt"] != stamp ||
		grants[1]["client"] != "b" || grants[2]["client"] != "a" {
		t.Fatal("controller order does not match the recorded approvals")
	}
	db, err := appdb.Open(m.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err := db.QueryRow("SELECT value FROM host_grants WHERE service=? AND client=?", service.URL, "c").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var persisted Grant
	if err := json.Unmarshal(raw, &persisted); err != nil || persisted.AuthorizedAt != stamp {
		t.Fatalf("wrong persisted approval time: %d, %v", persisted.AuthorizedAt, err)
	}
}
