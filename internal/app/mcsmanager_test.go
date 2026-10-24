package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newMCSAccount(t *testing.T, s *Service, endpoint string) Account {
	t.Helper()
	a, err := s.SaveAccount(Account{
		Name: "panel", Provider: ProviderMCS, Endpoint: endpoint, Credentials: CredAPIKey,
	}, &Secrets{SecretAccessKey: "panel-key"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMCSMDaemonsAndFiles(t *testing.T) {
	var listQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "panel-key" {
			t.Errorf("apikey = %q", r.URL.Query().Get("apikey"))
		}
		switch r.URL.Path {
		case "/api/service/remote_services_list":
			io.WriteString(w, `{"status":200,"data":[{"uuid":"d1","ip":"10.0.0.1","port":24444,"available":true,"remarks":"Node One"}]}`)
		case "/api/files/list":
			listQuery = r.URL.RawQuery
			io.WriteString(w, `{"status":200,"data":{"items":[
				{"name":"world","size":0,"time":"Fri Jun 07 2024 08:53:34 GMT+0800","mode":755,"type":0},
				{"name":"server.jar","size":42,"time":"Thu Jun 06 2024 18:25:14 GMT+0800","mode":644,"type":1}],
				"page":0,"pageSize":10,"total":2,"absolutePath":"/opt/mcsmanager/daemon/data/InstanceData/i1/srv"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	s := newTestService(t, nil)
	a := newMCSAccount(t, s, srv.URL)
	ctx := context.Background()

	ds, err := s.MCSDaemons(ctx, a.ID)
	if err != nil || len(ds) != 1 || ds[0].ID != "d1" || ds[0].Name != "Node One" || ds[0].Address != "10.0.0.1:24444" {
		t.Fatalf("daemons = %+v, err = %v", ds, err)
	}

	page, err := s.MCSFiles(ctx, a.ID, "d1", "i1", "/srv", 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if listQuery == "" || !strings.Contains(listQuery, "daemonId=d1") || !strings.Contains(listQuery, "uuid=i1") || !strings.Contains(listQuery, "target=%2Fsrv") {
		t.Errorf("list query = %q", listQuery)
	}
	if len(page.Entries) != 2 || page.Path != "/srv" || page.Entries[0].Path != "/srv/world" || !page.Entries[0].Folder {
		t.Fatalf("page = %+v", page)
	}
	if page.Entries[1].Path != "/srv/server.jar" || page.Entries[1].Folder || page.Entries[1].Size != 42 {
		t.Fatalf("file = %+v", page.Entries[1])
	}
}

func TestMCSMDownloadDoesNotOverwrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/files/download":
			if r.URL.Query().Get("file_name") != "/world.zip" {
				t.Errorf("file_name = %q", r.URL.Query().Get("file_name"))
			}
			json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]string{"password": "pw", "addr": r.Host}})
		case strings.HasPrefix(r.URL.Path, "/download/pw/"):
			io.WriteString(w, "world-bytes")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	s := newTestService(t, nil)
	a := newMCSAccount(t, s, srv.URL)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "world.zip"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.MCSDownload(context.Background(), a.ID, "d1", "i1", "/world.zip", dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "world (1).zip" {
		t.Fatalf("downloaded to %q", got)
	}
	b, _ := os.ReadFile(got)
	if string(b) != "world-bytes" {
		t.Fatalf("content = %q", b)
	}
	if old, _ := os.ReadFile(filepath.Join(dir, "world.zip")); string(old) != "old" {
		t.Fatalf("existing file changed: %q", old)
	}
}

func TestMCSUploadSendsFiles(t *testing.T) {
	var uploaded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/files/upload":
			if r.URL.Query().Get("upload_dir") != "/srv" {
				t.Errorf("upload_dir = %q", r.URL.Query().Get("upload_dir"))
			}
			json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]string{"password": "pw", "addr": r.Host}})
		case r.URL.Path == "/upload/pw":
			if r.URL.Query().Get("overwrite") != "false" {
				t.Errorf("overwrite = %q", r.URL.Query().Get("overwrite"))
			}
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

	s := newTestService(t, nil)
	a := newMCSAccount(t, s, srv.URL)
	local := filepath.Join(t.TempDir(), "server.properties")
	if err := os.WriteFile(local, []byte("motd=Pail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := s.MCSUpload(context.Background(), a.ID, "d1", "i1", "/srv", []string{local})
	if err != nil || n != 1 {
		t.Fatalf("upload = %d, %v", n, err)
	}
	if uploaded != "server.properties:motd=Pail\n" {
		t.Fatalf("uploaded = %q", uploaded)
	}
}

func TestMCSOperationsRequireTheBackendCapability(t *testing.T) {
	s := newTestService(t, nil)
	s3a, err := s.SaveAccount(staticAccount("s3", "http://h"), &Secrets{SecretAccessKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MCSDaemons(context.Background(), s3a.ID); !IsKind(err, ErrUnsupported) {
		t.Fatalf("MCSDaemons on s3 = %v, want Unsupported", err)
	}
	if _, err := s.MCSFiles(context.Background(), s3a.ID, "d", "i", "/", 0, 10, ""); !IsKind(err, ErrUnsupported) {
		t.Fatalf("MCSFiles on s3 = %v, want Unsupported", err)
	}
}

func TestMCSAccountValidationAndConnection(t *testing.T) {
	s := newTestService(t, nil)
	if _, err := s.SaveAccount(Account{Name: "bad", Provider: ProviderMCS, Endpoint: "http://h", Credentials: CredStatic, AccessKeyID: "a"}, &Secrets{SecretAccessKey: "x"}); err == nil {
		t.Fatal("an MCSManager account with static keys was accepted")
	}
	if _, err := s.SaveAccount(Account{Name: "bad", Provider: ProviderMCS, Endpoint: "http://h", Credentials: CredAPIKey, CDNURL: "https://cdn"}, &Secrets{SecretAccessKey: "x"}); err == nil {
		t.Fatal("an MCSManager account with a CDN was accepted")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/service/remote_services_list" {
			t.Errorf("path = %q", r.URL.Path)
		}
		io.WriteString(w, `{"status":200,"data":[]}`)
	}))
	t.Cleanup(srv.Close)
	if err := s.TestAccount(context.Background(), Account{Name: "panel", Provider: ProviderMCS, Endpoint: srv.URL, Credentials: CredAPIKey}, &Secrets{SecretAccessKey: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestMCSConnectionFallsBackForNonAdminKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/remote_services_list":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "API access is disabled")
		case "/api/auth":
			if r.URL.Query().Get("advanced") != "" {
				t.Errorf("connection test asked for advanced data")
			}
			io.WriteString(w, `{"status":200,"data":{"uuid":"u1","userName":"alice","permission":1,
				"instances":[{"daemonId":"d1","instanceUuid":"i1"}]}}`)
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	s := newTestService(t, nil)
	if err := s.TestAccount(context.Background(), Account{Name: "panel", Provider: ProviderMCS, Endpoint: srv.URL, Credentials: CredAPIKey}, &Secrets{SecretAccessKey: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestMCSNonAdminListsAssignedInstances(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/remote_services_list", "/api/service/remote_service_instances":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "administrator only")
		case "/api/auth":
			if r.URL.Query().Get("advanced") != "true" {
				t.Errorf("advanced = %q", r.URL.Query().Get("advanced"))
			}
			io.WriteString(w, `{"status":200,"data":{"uuid":"u1","userName":"alice","permission":1,
				"instances":[
					{"daemonId":"d1","instanceUuid":"i1","nickname":"Survival","hostIp":"10.0.0.1:24444","remarks":"Node One","status":3},
					{"daemonId":"d1","instanceUuid":"i2","nickname":"Creative","hostIp":"10.0.0.1:24444","remarks":"Node One","status":1},
					{"daemonId":"d2","instanceUuid":"i3","nickname":"","hostIp":"10.0.0.2:24445","remarks":"","status":0}
				]}}`)
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	s := newTestService(t, nil)
	a := newMCSAccount(t, s, srv.URL)
	ctx := context.Background()

	ds, err := s.MCSDaemons(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]MCSDaemon{}
	for _, d := range ds {
		byID[d.ID] = d
	}
	if len(ds) != 2 ||
		byID["d1"].Name != "Node One" || byID["d1"].Address != "10.0.0.1:24444" ||
		byID["d2"].Name != "10.0.0.2:24445" || byID["d2"].Address != "10.0.0.2:24445" {
		t.Fatalf("daemons = %+v", ds)
	}

	ins, err := s.MCSInstances(ctx, a.ID, "d1", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 2 || ins[0].UUID != "i1" || ins[0].Name != "Survival" || ins[0].Status != 3 ||
		ins[1].UUID != "i2" || ins[1].Name != "Creative" || ins[1].Status != 1 {
		t.Fatalf("instances = %+v", ins)
	}

	page, err := s.MCSInstances(ctx, a.ID, "d1", 2, 1)
	if err != nil || len(page) != 1 || page[0].UUID != "i2" {
		t.Fatalf("page 2 = %+v, err = %v", page, err)
	}

	fallback, err := s.MCSInstances(ctx, a.ID, "d2", 1, 50)
	if err != nil || len(fallback) != 1 || fallback[0].UUID != "i3" || fallback[0].Name != "i3" {
		t.Fatalf("unnamed instance = %+v, err = %v", fallback, err)
	}
}

func TestMCSAccessDeniedKeepsThePanelReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "API Key is not enabled")
	}))
	t.Cleanup(srv.Close)
	s := newTestService(t, nil)
	a := newMCSAccount(t, s, srv.URL)
	_, err := s.MCSDaemons(context.Background(), a.ID)
	if !IsKind(err, ErrAccessDenied) {
		t.Fatalf("err = %v, want AccessDenied", err)
	}
	if !strings.Contains(err.Error(), "API Key is not enabled") {
		t.Fatalf("err = %v, want the panel's reason", err)
	}
}

func TestMCSFilesKeepInstanceRelativePaths(t *testing.T) {
	var targets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("target")
		targets = append(targets, target)
		hostPath := "/opt/mcsmanager/daemon/data/InstanceData/i1"
		if target != "/" {
			hostPath += target
		}
		json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]any{
			"items":        []map[string]any{{"name": "yeqing.jpeg", "size": 226, "time": "now", "mode": 644, "type": 1}},
			"page":         0,
			"pageSize":     50,
			"total":        1,
			"absolutePath": hostPath + "/",
		}})
	}))
	t.Cleanup(srv.Close)

	s := newTestService(t, nil)
	a := newMCSAccount(t, s, srv.URL)
	ctx := context.Background()

	page, err := s.MCSFiles(ctx, a.ID, "d1", "i1", "/", 0, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Path != "/" || len(page.Entries) != 1 || page.Entries[0].Path != "/yeqing.jpeg" {
		t.Fatalf("page = %+v", page)
	}
	if _, err := s.MCSFiles(ctx, a.ID, "d1", "i1", page.Path, 0, 50, ""); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0] != "/" || targets[1] != "/" {
		t.Fatalf("targets = %v, want the relative root twice", targets)
	}
}
