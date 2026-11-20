package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestInstallScriptNamesThisServerAndNeedsNoLogin(t *testing.T) {
	_, srv, _, _ := newTestServer(t)

	resp, body := get(t, srv.URL+"/install.sh", nil) // no cookie, no Authorization
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(body, "__SERVER__") {
		t.Error("the placeholder was not filled in everywhere")
	}
	if !strings.Contains(body, "SERVER='"+srv.URL+"'") {
		t.Errorf("the script does not name this server (%s):\n%s", srv.URL, body)
	}
	if !strings.HasPrefix(body, "#!/bin/sh\n") {
		t.Errorf("the script does not start with a shell line: %q", body[:min(40, len(body))])
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// A truncated download must do nothing: everything runs from main, which is the
// last line, so a script cut anywhere before the end never starts it.
func TestInstallScriptCallsMainOnlyOnItsLastLine(t *testing.T) {
	lines := strings.Split(strings.TrimRight(installScript, "\n"), "\n")
	if last := lines[len(lines)-1]; last != `main "$@"` {
		t.Errorf("the last line is %q, want it to be the call of main", last)
	}
	for i, line := range lines[:len(lines)-1] {
		if strings.TrimSpace(line) == `main "$@"` {
			t.Errorf("line %d calls main before the end of the script", i+1)
		}
	}
}

// The address comes from the Host header, which a client chooses. Anything that
// could end a quote or start a command must never reach the script.
func TestInstallScriptRefusesAnAddressThatCouldInjectCode(t *testing.T) {
	_, srv, _, _ := newTestServer(t)
	for _, host := range []string{"x';id;'", "a b", "evil.example/$(id)", "a`id`", "h:80;rm", `a"b`} {
		req, err := http.NewRequest("GET", srv.URL+"/install.sh", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue // the server refused the Host header outright: also fine
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("Host %q: the script was served", host)
		}
	}
}

func TestServerAddressCheck(t *testing.T) {
	for addr, want := range map[string]bool{
		"https://headnode.example.com": true,
		"http://127.0.0.1:8080":        true,
		"http://localhost:8080":        true,
		"https://192.168.1.10":         true,
		"http://[::1]:8080":            true,
		"https://x":                    true,
		"ftp://x":                      false,
		"https://":                     false,
		"https://a b":                  false,
		"https://a/b":                  false,
		"https://a;b":                  false,
		"https://a'b":                  false,
		"https://a$b":                  false,
		"https://-x.com":               false,
	} {
		if got := serverAddressRe.MatchString(addr); got != want {
			t.Errorf("serverAddressRe(%q) = %v, want %v", addr, got, want)
		}
	}
}

// The command the dashboard shows is the one that uses it.
func TestAddHostCommandIsTheOneLinerThatFetchesTheInstaller(t *testing.T) {
	w := newPermWorld(t)
	var reply tokenReply
	if code := sendJSON(t, w.clients["root"], w.srv, "POST", "/api/enroll-tokens", map[string]any{}, &reply); code != http.StatusCreated {
		t.Fatalf("status = %d", code)
	}
	want := "curl -fsSL " + w.srv.URL + "/install.sh | sudo sh -s -- --token " + reply.Token
	if reply.InstallCommand != want {
		t.Errorf("install_command = %q, want %q", reply.InstallCommand, want)
	}
}
