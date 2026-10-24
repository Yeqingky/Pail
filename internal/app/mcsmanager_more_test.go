package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCSFileMutationsReachThePanel(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/files/" { // read or write text
			io.WriteString(w, `{"status":200,"data":"motd=Pail"}`)
			return
		}
		io.WriteString(w, `{"status":200,"data":true}`)
	}))
	t.Cleanup(srv.Close)

	s := newTestService(t, nil)
	a := newMCSAccount(t, s, srv.URL)
	ctx := context.Background()
	checks := []struct {
		name string
		call func() error
	}{
		{"mkdir", func() error { return s.MCSMkdir(ctx, a.ID, "d", "i", "/world") }},
		{"touch", func() error { return s.MCSTouch(ctx, a.ID, "d", "i", "/eula.txt") }},
		{"delete", func() error { return s.MCSDelete(ctx, a.ID, "d", "i", []string{"/eula.txt"}) }},
		{"copy", func() error { return s.MCSCopy(ctx, a.ID, "d", "i", [][2]string{{"/a", "/b"}}) }},
		{"move", func() error { return s.MCSMove(ctx, a.ID, "d", "i", [][2]string{{"/a", "/c"}}) }},
		{"chmod", func() error { return s.MCSChmod(ctx, a.ID, "d", "i", "/server.jar", 755, false) }},
		{"compress", func() error { return s.MCSCompress(ctx, a.ID, "d", "i", "/backup.zip", []string{"/world"}) }},
		{"decompress", func() error { return s.MCSDecompress(ctx, a.ID, "d", "i", "/backup.zip", "/restore", "gbk") }},
		{"write text", func() error { return s.MCSWriteText(ctx, a.ID, "d", "i", "/server.properties", "motd=Pail") }},
	}
	for _, c := range checks {
		if err := c.call(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	text, err := s.MCSReadText(ctx, a.ID, "d", "i", "/server.properties")
	if err != nil || text != "motd=Pail" {
		t.Fatalf("read text = %q, %v", text, err)
	}
	if len(paths) != len(checks)+1 {
		t.Fatalf("requests = %v", paths)
	}
}

func TestMCSFileStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/files/status" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]any{
			"instanceFileTask": 1, "globalFileTask": 2, "platform": "linux",
			"downloadTasks": []map[string]any{{"taskId": "t1", "path": "/a.zip", "total": 10, "current": 5, "status": 0}},
		}})
	}))
	t.Cleanup(srv.Close)
	s := newTestService(t, nil)
	a := newMCSAccount(t, s, srv.URL)
	st, err := s.MCSFileStatus(context.Background(), a.ID, "d", "i")
	if err != nil {
		t.Fatal(err)
	}
	if st.InstanceFileTask != 1 || st.GlobalFileTask != 2 || st.Platform != "linux" || len(st.DownloadTasks) != 1 || st.DownloadTasks[0].TaskID != "t1" {
		t.Fatalf("status = %+v", st)
	}
}
