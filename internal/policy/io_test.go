package policy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestROAExportFreshnessAndAtomicSnapshot(t *testing.T) {
	now := fixedTime()
	raw := fmt.Sprintf(`{"metadata":{"generated":%d},"roas":[{"prefix":"192.0.2.0/24","asn":"AS64501"}]}`, now.Add(-10*time.Minute).Unix())
	s, err := ParseROAExport(strings.NewReader(raw), "trusted-validator", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.ROAs[0].MaxLength != 24 || !s.ExpiresAt.Equal(now.Add(50*time.Minute)) {
		t.Fatal("upstream age was reset on fetch")
	}
	for _, bad := range []string{`{"roas":[]}`, fmt.Sprintf(`{"metadata":{"generated":%d},"roas":[]}`, now.Add(-2*time.Hour).Unix()), `{"metadata":{"generated":0},"roas":null}`} {
		if _, err := ParseROAExport(strings.NewReader(bad), "test", time.Hour, now); err == nil {
			t.Fatal("unaged/stale export accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err = WriteSnapshot(path, s); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot not private")
	}
	var saved ROASnapshot
	if err = ReadJSON(path, &saved); err != nil || len(saved.ROAs) != 1 {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "symlink")
	if err = os.Symlink(path, target); err != nil {
		t.Fatal(err)
	}
	if err = WriteSnapshot(target, s); err == nil {
		t.Fatal("snapshot symlink overwritten")
	}
}
func TestExplicitNetworkCollectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"generated": time.Now().Unix()}, "roas": []map[string]any{{"prefix": "192.0.2.0/24", "maxLength": 24, "asn": "AS64501"}}})
	}))
	defer server.Close()
	if _, err := FetchROAs(context.Background(), server.URL, time.Hour, false); err == nil {
		t.Fatal("HTTP used without explicit opt-in")
	}
	s, err := FetchROAs(context.Background(), server.URL, time.Hour, true)
	if err != nil || len(s.ROAs) != 1 {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- line
		fmt.Fprint(conn, "route: 192.0.2.0/24\norigin: AS64501\n")
	}()
	client := WhoisClient{Address: listener.Addr().String(), Sources: []string{"RIPE"}, Timeout: time.Second}
	result, err := client.Query(context.Background(), "-r -T route,route6 -i origin AS64501")
	if err != nil || !strings.Contains(result, "AS64501") {
		t.Fatal(err)
	}
	if line := <-got; line != "-s RIPE -r -T route,route6 -i origin AS64501\r\n" {
		t.Fatalf("unexpected WHOIS wire query: %q", line)
	}
}
