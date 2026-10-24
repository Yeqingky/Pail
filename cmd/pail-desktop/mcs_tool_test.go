package main

import (
	"io"
	"net/http"
	"net/http/httptest"
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
				"page":0,"pageSize":50,"total":2,"absolutePath":"/srv"}}`)
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

	if h.a.tool != "mcs" {
		t.Fatalf("tool = %q, want mcs", h.a.tool)
	}
	h.mustHave("Node One", "Survival", "/srv", "world", "server.jar")
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
