package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"headnode/protocol"
)

func TestSetVNC(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	path := "/api/hosts/" + web + "/vnc"

	// Until the agent says it has a VNC server it can control, there is nothing to switch.
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", path, map[string]any{"enabled": false}, nil); code != http.StatusConflict {
		t.Errorf("before any heartbeat: status = %d, want 409", code)
	}
	sendMessage(t, control, protocol.TypeHeartbeat, protocol.Heartbeat{VNC: true, VNCControl: true})
	waitFor(t, "the heartbeat", func() bool { _, hb := w.s.agents.status(web); return hb != nil && hb.VNCControl })

	// alice is an operator: allowed, and the agent is told what to do.
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", path, map[string]any{"enabled": false}, nil); code != http.StatusAccepted {
		t.Fatalf("alice: status = %d, want 202", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := control.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeVNCSet {
		t.Fatalf("control message = %s, want a vnc_set (err %v)", data, err)
	}
	var set protocol.VNCSet
	if err := json.Unmarshal(env.Data, &set); err != nil || set.Enabled {
		t.Errorf("vnc_set = %s, want enabled false (err %v)", env.Data, err)
	}
	if !hasAudit(t, w.s, "vnc_off") {
		t.Error("no vnc_off entry in the audit log")
	}

	// bob is a viewer; carol cannot see the host at all.
	if code := sendJSON(t, w.clients["bob"], w.srv, "POST", path, map[string]any{"enabled": true}, nil); code != http.StatusForbidden {
		t.Errorf("viewer: status = %d, want 403", code)
	}
	if code := sendJSON(t, w.clients["carol"], w.srv, "POST", path, map[string]any{"enabled": true}, nil); code != http.StatusNotFound {
		t.Errorf("outsider: status = %d, want 404", code)
	}

	// An agent that cannot control a VNC server is not asked to.
	sendMessage(t, control, protocol.TypeHeartbeat, protocol.Heartbeat{VNCControl: false})
	waitFor(t, "the heartbeat", func() bool { _, hb := w.s.agents.status(web); return hb != nil && !hb.VNCControl })
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", path, map[string]any{"enabled": true}, nil); code != http.StatusConflict {
		t.Errorf("no controllable VNC: status = %d, want 409", code)
	}
}

// D-64: the VNC password the agent reports reaches only someone who may open the
// screen, with the tunnel, and never the host list, which viewers see too.
func TestVNCPasswordGoesOnlyWithTheTunnel(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	const password = "Zx9Qw2Lm"
	sendMessage(t, control, protocol.TypeHeartbeat, protocol.Heartbeat{VNC: true, VNCControl: true, VNCPassword: password})
	waitFor(t, "the heartbeat", func() bool { return w.s.agents.vncPassword(web) == password })

	// bob is a viewer: the host list (and the host's own page) must not carry it.
	for _, path := range []string{"/api/hosts", "/api/hosts/" + web} {
		resp, err := w.clients["bob"].Get(w.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), password) {
			t.Errorf("GET %s shows the VNC password to a viewer", path)
		}
	}

	tunnel := map[string]any{"kind": "tunnel", "params": map[string]any{"target": "127.0.0.1:5900"}}
	// bob may not open a screen at all.
	if code := sendJSON(t, w.clients["bob"], w.srv, "POST", "/api/hosts/"+web+"/sessions", tunnel, nil); code != http.StatusForbidden {
		t.Errorf("bob's screen tunnel: status = %d, want 403", code)
	}
	// alice, an operator, gets it with the tunnel.
	var reply map[string]any
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/hosts/"+web+"/sessions", tunnel, &reply); code != http.StatusCreated {
		t.Fatalf("alice's screen tunnel: status = %d, want 201", code)
	}
	if reply["vnc_password"] != password {
		t.Errorf("alice's reply has vnc_password %v, want %q", reply["vnc_password"], password)
	}
	// And the audit log never holds it.
	var leaked int
	if err := w.s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE ?`, "%"+password+"%").Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Error("the VNC password is in the audit log")
	}
}
