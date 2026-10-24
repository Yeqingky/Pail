package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/DejavuMoe/Pail/internal/app"
)

func TestMCSMAccountFormUsesAPIFields(t *testing.T) {
	h := newHarness(t, "light")
	h.a.openAccountForm(false)
	h.a.af.provider = providerMCS
	h.settle()
	h.mustHave("Endpoint", "API key", "Skip TLS verification")
	h.mustNotHave("Access key ID", "CDN URL")

	h.a.af.name, h.a.af.endpoint, h.a.af.secret = "Panel", "https://panel.example", "key"
	ac, sec := h.a.af.build()
	if ac.Provider != core.ProviderMCS || ac.Credentials != core.CredAPIKey || ac.Endpoint != "https://panel.example" {
		t.Fatalf("built account = %+v", ac)
	}
	if sec == nil || sec.SecretAccessKey != "key" {
		t.Fatalf("built secrets = %+v", sec)
	}
	if errs := h.a.af.fieldErrors(); len(errs) != 0 {
		t.Fatalf("field errors = %v", errs)
	}
}

func TestMCSMToolListsInstancesAndFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "panel-key" {
			t.Errorf("apikey = %q", r.URL.Query().Get("apikey"))
		}
		switch r.URL.Path {
		case "/api/service/remote_services_list":
			io.WriteString(w, `{"status":200,"data":[{"uuid":"d1","ip":"10.0.0.1","port":24444,"available":true,"remarks":"Node One"}]}`)
		case "/api/service/remote_service_instances":
			io.WriteString(w, `{"status":200,"data":{"page":1,"pageSize":10,"maxPage":1,"data":[{"instanceUuid":"i1","status":3,"config":{"nickname":"Survival"}}]}}`)
		case "/api/files/list":
			io.WriteString(w, `{"status":200,"data":{"items":[
				{"name":"world","size":0,"time":"Fri Jun 07 2024 08:53:34 GMT+0800","mode":755,"type":0},
				{"name":"server.jar","size":42,"time":"Thu Jun 06 2024 18:25:14 GMT+0800","mode":644,"type":1}],
				"page":0,"pageSize":50,"total":2,"absolutePath":"/opt/mcsmanager/daemon/data/InstanceData/i1/"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	h := newHarness(t, "light")
	acct, err := h.env.Svc.SaveAccount(core.Account{
		Name: "Panel", Provider: core.ProviderMCS, Endpoint: srv.URL, Credentials: core.CredAPIKey,
	}, &core.Secrets{SecretAccessKey: "panel-key"})
	if err != nil {
		t.Fatal(err)
	}
	h.a.refreshAccounts()
	h.a.selectAccount(acct.ID)
	h.waitFor("MCSManager files", func() bool {
		return len(h.a.tl.mcs.daemons) == 1 && len(h.a.tl.mcs.files) == 2
	})
	h.settle()

	if h.a.tl.mcs.path != "/" || len(h.a.tl.mcs.files) != 2 || h.a.tl.mcs.files[0].Path != "/world" {
		t.Fatalf("path = %q, files = %+v", h.a.tl.mcs.path, h.a.tl.mcs.files)
	}
	if h.a.tool != "mcs" {
		t.Fatalf("tool = %q, want mcs", h.a.tool)
	}
	h.mustHave("Node One", "Survival", "world", "server.jar")
	if h.a.visible(h.a.cmd("tool.cloudfront")) {
		t.Fatal("CloudFront is visible for an MCSManager account")
	}
	if h.a.visible(h.a.cmd("tool.sync")) {
		t.Fatal("the S3 sync tool is visible for an MCSManager account")
	}
	if !h.a.visible(h.a.cmd("tool.mcs")) {
		t.Fatal("the MCSManager tool is hidden for its own account")
	}
}

func TestMCSMToolFallsBackForNonAdminKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "panel-key" {
			t.Errorf("apikey = %q", r.URL.Query().Get("apikey"))
		}
		switch r.URL.Path {
		case "/api/service/remote_services_list", "/api/service/remote_service_instances":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "administrator only")
		case "/api/auth":
			if r.URL.Query().Get("advanced") != "true" {
				t.Errorf("advanced = %q", r.URL.Query().Get("advanced"))
			}
			io.WriteString(w, `{"status":200,"data":{"uuid":"u1","userName":"alice","permission":1,
				"instances":[{"daemonId":"d1","instanceUuid":"i1","nickname":"Survival","hostIp":"10.0.0.1:24444","remarks":"Node One","status":3}]}}`)
		case "/api/files/list":
			io.WriteString(w, `{"status":200,"data":{"items":[
				{"name":"world","size":0,"time":"Fri Jun 07 2024 08:53:34 GMT+0800","mode":755,"type":0}],
				"page":0,"pageSize":50,"total":1,"absolutePath":"/opt/mcsmanager/daemon/data/InstanceData/i1/"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	h := newHarness(t, "light")
	acct, err := h.env.Svc.SaveAccount(core.Account{
		Name: "Panel", Provider: core.ProviderMCS, Endpoint: srv.URL, Credentials: core.CredAPIKey,
	}, &core.Secrets{SecretAccessKey: "panel-key"})
	if err != nil {
		t.Fatal(err)
	}
	h.a.refreshAccounts()
	h.a.selectAccount(acct.ID)
	h.waitFor("MCSManager files for a non-admin key", func() bool {
		return len(h.a.tl.mcs.daemons) == 1 && len(h.a.tl.mcs.files) == 1
	})
	h.settle()

	if h.a.tl.mcs.path != "/" || len(h.a.tl.mcs.files) != 1 || h.a.tl.mcs.files[0].Path != "/world" {
		t.Fatalf("path = %q, files = %+v", h.a.tl.mcs.path, h.a.tl.mcs.files)
	}
	if h.a.tl.mcs.daemons[0].Name != "Node One" || h.a.tl.mcs.instances[0].Name != "Survival" {
		t.Fatalf("daemons = %+v, instances = %+v", h.a.tl.mcs.daemons, h.a.tl.mcs.instances)
	}
	h.mustHave("Node One", "Survival", "world")
}

func TestMCSAccessDeniedTextExplainsPanelAPI(t *testing.T) {
	err := &core.OpError{Op: "TestAccount", Kind: core.ErrAccessDenied, Message: "API access is disabled"}
	got := mcsErrorText(err)
	if got == humanError(err) || !strings.Contains(got, "API access is disabled") {
		t.Fatalf("text = %q", got)
	}
}

func TestMCSMToolUploadShowsATransferTask(t *testing.T) {
	var uploaded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/remote_services_list":
			io.WriteString(w, `{"status":200,"data":[{"uuid":"d1","ip":"10.0.0.1","port":24444,"available":true,"remarks":"Node One"}]}`)
		case "/api/service/remote_service_instances":
			io.WriteString(w, `{"status":200,"data":{"page":1,"pageSize":10,"maxPage":1,"data":[{"instanceUuid":"i1","status":3,"config":{"nickname":"Survival"}}]}}`)
		case "/api/files/list":
			io.WriteString(w, `{"status":200,"data":{"items":[],"page":0,"pageSize":100,"total":0,"absolutePath":"/opt/mcsmanager/daemon/data/InstanceData/i1/"}}`)
		case "/api/files/upload":
			if r.URL.Query().Get("upload_dir") != "/" {
				t.Errorf("upload_dir = %q", r.URL.Query().Get("upload_dir"))
			}
			io.WriteString(w, `{"status":200,"data":{"password":"pw","addr":"ws://`+r.Host+`"}}`)
		case "/upload/pw":
			f, h, err := r.FormFile("file")
			if err != nil {
				t.Errorf("file field: %v", err)
				return
			}
			defer f.Close()
			b, _ := io.ReadAll(f)
			uploaded = h.Filename + ":" + string(b)
			io.WriteString(w, "OK")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	h := newHarness(t, "light")
	acct, err := h.env.Svc.SaveAccount(core.Account{
		Name: "Panel", Provider: core.ProviderMCS, Endpoint: srv.URL, Credentials: core.CredAPIKey,
	}, &core.Secrets{SecretAccessKey: "panel-key"})
	if err != nil {
		t.Fatal(err)
	}
	h.a.refreshAccounts()
	h.a.selectAccount(acct.ID)
	h.waitFor("MCSManager files", func() bool {
		return len(h.a.tl.mcs.daemons) == 1 && len(h.a.tl.mcs.instances) == 1 && !h.a.tl.mcs.loading
	})

	local := filepath.Join(t.TempDir(), "eula.txt")
	if err := os.WriteFile(local, []byte("eula=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.a.pick = func(bool) []string { return []string{local} }
	h.a.mcsPickUpload()
	h.waitFor("MCS upload task", func() bool {
		for _, tk := range h.a.tasks {
			if tk.Title == "Upload eula.txt" && tk.Status == core.StatusSucceeded {
				return len(h.a.watches) == 0 && !h.a.tl.mcs.loading
			}
		}
		return false
	})
	h.settle()

	if !h.a.transfersOpen {
		t.Fatal("the transfers panel did not open")
	}
	if uploaded != "eula.txt:eula=true\n" {
		t.Fatalf("uploaded = %q", uploaded)
	}
	h.a.transTab = 1 // completed
	h.settle()
	h.mustHave("Upload eula.txt", "→ /eula.txt")
}

func TestMCSMAccountFormSwitchesToPassword(t *testing.T) {
	h := newHarness(t, "light")
	h.a.openAccountForm(false)
	h.a.af.provider = providerMCS
	h.settle()
	h.mustHave("Sign-in method", "API key", "Endpoint")

	h.a.af.mcsAuth = mcsAuthPassword
	h.settle()
	h.mustHave("Username", "Password", "Two-factor code")

	h.a.af.name, h.a.af.endpoint = "Panel", "https://panel.example"
	h.a.af.access, h.a.af.secret, h.a.af.mcsCode = "alice", "pw", "123456"
	ac, sec := h.a.af.build()
	if ac.Provider != core.ProviderMCS || ac.Credentials != core.CredPassword || ac.AccessKeyID != "alice" || ac.LoginCode != "123456" {
		t.Fatalf("built account = %+v", ac)
	}
	if sec == nil || sec.SecretAccessKey != "pw" {
		t.Fatalf("built secrets = %+v", sec)
	}
	if errs := h.a.af.fieldErrors(); len(errs) != 0 {
		t.Fatalf("field errors = %v", errs)
	}

	h.a.af.access, h.a.af.secret = "", ""
	if errs := h.a.af.fieldErrors(); errs["username"] == "" || errs["password"] == "" {
		t.Fatalf("missing field errors = %v", errs)
	}
}

func TestMCSMToolPromptsForTwoFactorCode(t *testing.T) {
	var codeSeen string
	token := "tok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			codeSeen = body["code"]
			if codeSeen == "" {
				io.WriteString(w, `{"status":200,"data":"NEED_2FA"}`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "cookie", Path: "/"})
			io.WriteString(w, `{"status":200,"data":"`+token+`"}`)
		case "/api/service/remote_services_list":
			if r.URL.Query().Get("token") != token {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"status":403,"data":"token error"}`)
				return
			}
			io.WriteString(w, `{"status":200,"data":[{"uuid":"d1","ip":"10.0.0.1","port":24444,"available":true,"remarks":"Node One"}]}`)
		case "/api/service/remote_service_instances":
			io.WriteString(w, `{"status":200,"data":{"page":1,"pageSize":10,"maxPage":1,"data":[{"instanceUuid":"i1","status":3,"config":{"nickname":"Survival"}}]}}`)
		case "/api/files/list":
			io.WriteString(w, `{"status":200,"data":{"items":[],"page":0,"pageSize":100,"total":0,"absolutePath":"/opt/mcsmanager/daemon/data/InstanceData/i1/"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	h := newHarness(t, "light")
	acct, err := h.env.Svc.SaveAccount(core.Account{
		Name: "Panel", Provider: core.ProviderMCS, Endpoint: srv.URL, Credentials: core.CredPassword, AccessKeyID: "alice",
	}, &core.Secrets{SecretAccessKey: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	h.a.refreshAccounts()
	h.a.selectAccount(acct.ID)
	h.waitFor("two-factor dialog", func() bool { return h.a.tl.mcs.codeOpen })
	h.mustHave("Two-factor code")
	if codeSeen != "" {
		t.Fatalf("first login sent code %q", codeSeen)
	}

	h.a.tl.mcs.code = "123456"
	h.a.mcsSubmitCode()
	h.waitFor("panel after two-factor", func() bool {
		return !h.a.tl.mcs.codeOpen && len(h.a.tl.mcs.daemons) == 1 && len(h.a.tl.mcs.instances) == 1
	})
	if codeSeen != "123456" {
		t.Fatalf("login code = %q", codeSeen)
	}
	h.mustHave("Node One", "Survival")
}
