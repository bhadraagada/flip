package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecentLogs(t *testing.T) {
	for _, tc := range []struct {
		data string
		n    int
		want string
	}{
		{"one\ntwo\nthree\n", 2, "two\nthree\n"}, {"one\ntwo\nthree", 1, "three"},
		{"one\n", 0, ""}, {"", 10, ""}, {"\n\n", 1, "\n"},
		{strings.Repeat("long", 5000) + "\nlast\n", 1, "last\n"},
	} {
		path := filepath.Join(t.TempDir(), "log")
		os.WriteFile(path, []byte(tc.data), 0600)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := recentLines(f, tc.n, &out); err != nil {
			t.Fatal(err)
		}
		offset, _ := f.Seek(0, io.SeekCurrent)
		f.Close()
		if out.String() != tc.want || offset != int64(len(tc.data)) {
			t.Fatalf("tail %d: %q offset %d", tc.n, out.String(), offset)
		}
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".flip"), 0700)
	path := filepath.Join(root, ".flip", "one-ui.log")
	os.WriteFile(path, []byte("old\n"), 0600)
	c := config{root: root, Worktrees: map[string]worktree{"one": {UI: service{Command: []string{"unused"}}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- logs(ctx, c, []string{"one", "ui", "-n", "0", "-f"}, writer); writer.Close() }()
	// Allow the follower to seek to the end before appending a new line.
	time.Sleep(100 * time.Millisecond)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("new\n")
	f.Close()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(reader, buf); err != nil || string(buf) != "new\n" {
		t.Fatal(string(buf), err)
	}
	cancel()
	reader.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := logs(context.Background(), c, []string{"../escape", "ui"}, io.Discard); err == nil {
		t.Fatal("accepted unknown worktree")
	}
}

func TestPickerAuthentication(t *testing.T) {
	m := newManager(context.Background(), config{ControlPort: 19091, Port: 19092, Worktrees: map[string]worktree{"one": {UI: service{Command: []string{"missing"}}}}})
	p := newPicker(m)
	origin := "http://" + address(m.c.ControlPort)
	call := func(path, body, session, from string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", origin+path, strings.NewReader(body))
		r.Header.Set("Origin", from)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+session)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	link, err := p.link()
	if err != nil {
		t.Fatal(err)
	}
	grant := strings.TrimSpace(strings.Split(link, "#")[1])
	body := `{"Grant":"` + grant + `"}`
	if w := call("/picker/session", body, "", "http://evil.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := call("/picker/session", body, "", origin)
	var login struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &login)
	if w.Code != 200 || login.Session == "" || login.Session == grant {
		t.Fatal(w.Code)
	}
	if w := call("/picker/session", body, "", origin); w.Code != 403 {
		t.Fatal("replayed grant", w.Code)
	}
	for _, from := range []string{"", "http://localhost:19091", "http://evil.example"} {
		if w := call("/picker/api", `{"Action":"use","Name":"one"}`, login.Session, from); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if w := call("/picker/api", `{"Action":"status"}`, "", origin); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("/picker/api", `{"Action":"down","Name":"one"}`, login.Session, origin); w.Code != 400 {
		t.Fatal("accepted extra capability", w.Code)
	}
	if w := call("/picker/api", `{"Action":"status"}`, login.Session, origin); w.Code != 200 || !strings.Contains(w.Body.String(), "stopped") {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(m.processes) != 0 || m.active.Load() != nil {
		t.Fatal("status mutated processes")
	}
	p.sessions[login.Session] = time.Now().Add(-time.Second)
	if w := call("/picker/api", `{"Action":"status"}`, login.Session, origin); w.Code != 403 {
		t.Fatal("expired session", w.Code)
	}
	p.grants[grant] = time.Now().Add(-time.Second)
	if w := call("/picker/session", body, "", origin); w.Code != 403 {
		t.Fatal("expired grant", w.Code)
	}
	r := httptest.NewRequest("GET", "http://evil.example/picker", nil)
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("accepted rebinding host")
	}
	r = httptest.NewRequest("GET", origin+"/picker", nil)
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), grant) || w.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("unsafe picker document")
	}
}

func TestDoctorDoesNotStartApps(t *testing.T) {
	root := t.TempDir()
	c := config{root: root, Port: freePort(t), ControlPort: freePort(t), APIPrefix: "/api", Worktrees: map[string]worktree{"broken": {UI: service{Command: []string{"flip-missing-executable-34724"}, Dir: root, Port: freePort(t)}}}}
	var out bytes.Buffer
	if err := doctor(c, &out); err == nil || !strings.Contains(out.String(), "install the executable") {
		t.Fatal(err, out.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".flip")); !os.IsNotExist(err) {
		t.Fatal("doctor created state")
	}
}
