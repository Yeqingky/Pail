package mcsmanager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, APIKey: "secret", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func TestListFilesSendsAuthAndParsesPage(t *testing.T) {
	var got *http.Request
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		io.WriteString(w, `{"status":200,"data":{
			"items":[{"name":"world","size":0,"time":"Fri Jun 07 2024 08:53:34 GMT+0800","mode":755,"type":0},
			         {"name":"server.jar","size":42,"time":"Thu Jun 06 2024 18:25:14 GMT+0800","mode":644,"type":1}],
			"page":0,"pageSize":10,"total":2,"absolutePath":"/opt/mcsmanager/daemon/data/InstanceData/i1/srv"},"time":1}`)
	})
	page, err := c.ListFiles(context.Background(), "d1", "i1", "/srv", 0, 10, "world")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/api/files/list" {
		t.Fatalf("path = %q", got.URL.Path)
	}
	q := got.URL.Query()
	for k, want := range map[string]string{"daemonId": "d1", "uuid": "i1", "target": "/srv", "page": "0", "page_size": "10", "file_name": "world", "apikey": "secret"} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if got.Header.Get("X-Requested-With") != "XMLHttpRequest" || !strings.HasPrefix(got.Header.Get("Content-Type"), "application/json") {
		t.Errorf("headers = %v", got.Header)
	}
	if len(page.Items) != 2 || page.Items[0].Name != "world" || !page.Items[0].Folder() || page.Items[1].Folder() {
		t.Fatalf("page = %+v", page)
	}
	if page.Target != "/srv" || page.Total != 2 {
		t.Fatalf("page metadata = %+v", page)
	}
}

func TestPanelErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		denied bool
		not    bool
		msg    string
	}{
		{"http 403", 403, `{"status":403,"data":"forbidden"}`, true, false, "forbidden"},
		{"json 403", 200, `{"status":403,"data":"no access"}`, true, false, "no access"},
		{"plain 403", 403, "API access is disabled", true, false, "API access is disabled"},
		{"http 404", 404, `{"status":404,"data":"missing"}`, false, true, "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			_, err := c.ListDaemons(context.Background())
			var ae *APIError
			if !asAPIError(err, &ae) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if ae.AccessDenied() != tc.denied || ae.NotFound() != tc.not || ae.Error() != tc.msg {
				t.Fatalf("error = %+v", ae)
			}
		})
	}
}

func asAPIError(err error, out **APIError) bool {
	ae, ok := err.(*APIError)
	if ok {
		*out = ae
	}
	return ok
}

func TestListInstancesAndDaemons(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/remote_services_list":
			io.WriteString(w, `{"status":200,"data":[{"uuid":"d1","ip":"10.0.0.1","port":24444,"available":true,"remarks":"node one"}]}`)
		case "/api/service/remote_service_instances":
			if r.URL.Query().Get("daemonId") != "d1" || r.URL.Query().Get("instance_name") != "survival" {
				t.Errorf("query = %v", r.URL.Query())
			}
			io.WriteString(w, `{"status":200,"data":{"page":1,"pageSize":10,"maxPage":1,"data":[{"instanceUuid":"i1","status":3,"started":2,"config":{"nickname":"Survival","cwd":"/srv/survival"}}]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	ds, err := c.ListDaemons(context.Background())
	if err != nil || len(ds) != 1 || ds[0].UUID != "d1" || ds[0].Port != 24444 || ds[0].Remarks != "node one" {
		t.Fatalf("daemons = %+v, err = %v", ds, err)
	}
	page, err := c.ListInstances(context.Background(), "d1", 1, 10, "survival")
	if err != nil || len(page.Data) != 1 || page.Data[0].UUID != "i1" || page.Data[0].Name() != "Survival" || page.Data[0].Status != 3 {
		t.Fatalf("instances = %+v, err = %v", page, err)
	}
}

func TestCurrentUser(t *testing.T) {
	var got *http.Request
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		io.WriteString(w, `{"status":200,"data":{"uuid":"u1","userName":"alice","permission":1,
			"instances":[{"daemonId":"d1","instanceUuid":"i1","nickname":"Survival",
			              "hostIp":"10.0.0.1:24444","remarks":"Node One","status":3}]},"time":1}`)
	})
	u, err := c.CurrentUser(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/api/auth" || got.URL.Query().Get("advanced") != "true" || got.URL.Query().Get("apikey") != "secret" {
		t.Fatalf("request = %s?%s", got.URL.Path, got.URL.RawQuery)
	}
	if u.UUID != "u1" || u.UserName != "alice" || u.Permission != 1 || len(u.Instances) != 1 {
		t.Fatalf("user = %+v", u)
	}
	in := u.Instances[0]
	if in.DaemonID != "d1" || in.UUID != "i1" || in.Nickname != "Survival" || in.HostIP != "10.0.0.1:24444" || in.Remarks != "Node One" || in.Status != 3 {
		t.Fatalf("instance = %+v", in)
	}
}

func TestNewTrimsTheAPIKey(t *testing.T) {
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.URL.Query().Get("apikey")
		io.WriteString(w, `{"status":200,"data":[]}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, APIKey: "  secret\n", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListDaemons(context.Background()); err != nil {
		t.Fatal(err)
	}
	if key != "secret" {
		t.Fatalf("apikey = %q", key)
	}
}

func TestPassportURLs(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {})
	p := Passport{Password: "pw", Addr: "127.0.0.1:24444"}
	u, err := c.DownloadURL(p, "/backup/world.zip")
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://127.0.0.1:24444/download/pw/backup/world.zip"; u != want {
		t.Fatalf("download URL = %q, want %q", u, want)
	}
	u, err = c.UploadURL(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://127.0.0.1:24444/upload/pw"; u != want {
		t.Fatalf("upload URL = %q, want %q", u, want)
	}
	p.Addr = "http://daemon.example/base"
	u, _ = c.DownloadURL(p, "a b.txt")
	if want := "http://daemon.example/base/download/pw/a%20b.txt"; u != want {
		t.Fatalf("escaped URL = %q, want %q", u, want)
	}
}

func TestUploadStreamsMultipart(t *testing.T) {
	var uploadName, uploadBody, overwrite string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files/upload":
			if r.URL.Query().Get("upload_dir") != "/srv/world" {
				t.Errorf("upload_dir = %q", r.URL.Query().Get("upload_dir"))
			}
			io.WriteString(w, `{"status":200,"data":{"password":"pw","addr":"`+r.Host+`"}}`)
		case "/upload/pw":
			overwrite = r.URL.Query().Get("overwrite")
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			f, h, err := r.FormFile("file")
			if err != nil {
				t.Errorf("file field: %v", err)
			} else {
				defer f.Close()
				uploadName = h.Filename
				b, _ := io.ReadAll(f)
				uploadBody = string(b)
			}
			io.WriteString(w, "OK")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	dir := t.TempDir()
	local := filepath.Join(dir, "eula.txt")
	if err := os.WriteFile(local, []byte("eula=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Upload(context.Background(), "d1", "i1", "/srv/world", local, false); err != nil {
		t.Fatal(err)
	}
	if uploadName != "eula.txt" || uploadBody != "eula=true\n" || overwrite != "false" {
		t.Fatalf("upload name=%q body=%q overwrite=%q", uploadName, uploadBody, overwrite)
	}
}

func TestFileMutationsSendExpectedBodies(t *testing.T) {
	type call struct {
		path, method string
		body         []byte
	}
	var calls []call
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.URL.Path, r.Method, b})
		io.WriteString(w, `{"status":200,"data":true}`)
	})
	ctx := context.Background()
	if err := c.Delete(ctx, "d1", "i1", []string{"/a", "/b"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Mkdir(ctx, "d1", "i1", "/world"); err != nil {
		t.Fatal(err)
	}
	if err := c.Touch(ctx, "d1", "i1", "/eula.txt"); err != nil {
		t.Fatal(err)
	}
	if err := c.Copy(ctx, "d1", "i1", [][2]string{{"/a", "/b"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Move(ctx, "d1", "i1", [][2]string{{"/a", "/c"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Chmod(ctx, "d1", "i1", "/server.jar", 755, false); err != nil {
		t.Fatal(err)
	}
	if err := c.Compress(ctx, "d1", "i1", "/backup.zip", []string{"/world"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Decompress(ctx, "d1", "i1", "/backup.zip", "/restore", "gbk"); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		method, path, body string
	}{
		{"DELETE", "/api/files", `{"targets":["/a","/b"]}`},
		{"POST", "/api/files/mkdir", `{"target":"/world"}`},
		{"POST", "/api/files/touch", `{"target":"/eula.txt"}`},
		{"POST", "/api/files/copy", `{"targets":[["/a","/b"]]}`},
		{"POST", "/api/files/move", `{"targets":[["/a","/c"]]}`},
		{"PUT", "/api/files/chmod", `{"chmod":755,"deep":false,"target":"/server.jar"}`},
		{"POST", "/api/files/compress", `{"code":"utf-8","source":"/backup.zip","targets":["/world"],"type":1}`},
		{"POST", "/api/files/compress", `{"code":"gbk","source":"/backup.zip","targets":"/restore","type":2}`},
	}
	if len(calls) != len(want) {
		t.Fatalf("%d calls, want %d", len(calls), len(want))
	}
	for i, c := range calls {
		if c.method != want[i].method || c.path != want[i].path || string(c.body) != want[i].body {
			t.Errorf("call %d = %s %s %s, want %s %s %s", i, c.method, c.path, c.body, want[i].method, want[i].path, want[i].body)
		}
	}
}

func TestTargetsKeepWindowsDrives(t *testing.T) {
	for in, want := range map[string]string{
		"": "/", "/world/": "/world", "world": "/world", `C:\srv\world`: "C:/srv/world", "C:/": "C:",
	} {
		if got := cleanTarget(in); got != want {
			t.Errorf("cleanTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTransportErrorsDoNotLeakTheAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL
	srv.Close()
	c, err := New(Config{Endpoint: endpoint, APIKey: "super-secret-key", HTTPClient: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListDaemons(context.Background())
	if err == nil {
		t.Fatal("a closed panel answered")
	}
	for _, secret := range []string{"super-secret-key", "apikey"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
}

func TestJSONBodyIsNotEscaped(t *testing.T) {
	// Guard the panel's requirement that a body is JSON, not form data.
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Errorf("body: %v", err)
		}
		io.WriteString(w, `{"status":200,"data":true}`)
	})
	if err := c.Mkdir(context.Background(), "d1", "i1", "/a&b"); err != nil {
		t.Fatal(err)
	}
}

func TestListFilesAlwaysSendsTheNameFilter(t *testing.T) {
	var q url.Values
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		io.WriteString(w, `{"status":200,"data":{"items":[],"page":0,"pageSize":10,"total":0}}`)
	})
	if _, err := c.ListFiles(context.Background(), "d1", "i1", "/", 0, 10, ""); err != nil {
		t.Fatal(err)
	}
	if !q.Has("file_name") || q.Get("file_name") != "" {
		t.Fatalf("file_name = %q, present = %v", q.Get("file_name"), q.Has("file_name"))
	}
}
