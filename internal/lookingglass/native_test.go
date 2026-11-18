package lookingglass

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func birdWire(verbose string) string {
	lines := strings.Split(strings.TrimPrefix(verbose, "0001 BIRD 2.17.5 ready.\n"), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "     ") {
			lines[i] = " " + line[5:]
		}
	}
	return strings.Join(lines, "\n") + "0000 \n"
}
func TestNativeBIRDClientsWithReadOnlyMockViews(t *testing.T) {
	for _, tc := range []struct{ env, platform, version string }{{"BIRD2_BIRDC", "bird2", "2.17.5"}, {"BIRD3_BIRDC", "bird3", "3.4.0"}} {
		binary := os.Getenv(tc.env)
		if binary == "" {
			t.Log(tc.env, "unset: native-client test skipped")
			continue
		}
		t.Run(tc.platform, func(t *testing.T) {
			dir := t.TempDir()
			socket := filepath.Join(dir, "bird.sock")
			listener, e := net.Listen("unix", socket)
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan struct{})
			var mu sync.Mutex
			commands := []string{}
			go func() {
				defer close(done)
				for {
					conn, e := listener.Accept()
					if e != nil {
						return
					}
					func() {
						defer conn.Close()
						conn.SetDeadline(time.Now().Add(10 * time.Second))
						conn.Write([]byte("0001 BIRD " + tc.version + " ready.\n"))
						reader := bufio.NewReader(conn)
						for {
							line, e := reader.ReadString('\n')
							if e != nil {
								return
							}
							line = strings.TrimSpace(line)
							mu.Lock()
							commands = append(commands, line)
							mu.Unlock()
							if line == "restrict" {
								conn.Write([]byte("0000 \n"))
								continue
							}
							reply := "9001 Unknown read-only mock query\n"
							switch {
							case line == "show status":
								reply = "1000-BIRD " + tc.version + "\n1011-Router ID is 192.0.2.1\n0013 Daemon is up and running\n"
							case strings.HasPrefix(line, "show protocols all"):
								reply = birdWire(birdIdentityFixture)
							case strings.HasPrefix(line, "show route"):
								raw := birdRouteFixture
								if strings.Contains(line, " export ") {
									raw = strings.ReplaceAll(raw, "[peer_a ", "[other_source ")
								}
								if tc.platform == "bird3" {
									raw = strings.NewReplacer("BGP.as_path", "bgp_path", "BGP.next_hop", "bgp_next_hop", "BGP.community", "bgp_community", "BGP.large_community", "bgp_large_community").Replace(raw)
								}
								reply = birdWire(raw)
							}
							conn.Write([]byte(reply))
							return
						}
					}()
				}
			}()
			defer func() { listener.Close(); <-done }()
			c := Config{RouterID: "router-a", Platform: tc.platform, Peers: map[string]string{"peer-a": "peer_a"}, Birdc: binary, BirdSocket: socket, AllowTable: true}
			for _, view := range []string{ViewAccepted, ViewFiltered, ViewPrePolicy, ViewExported, ViewTable} {
				q := queryFixture(tc.platform, view)
				q.ExpectedRouterID = "192.0.2.1"
				q.ExpectedRouterASN = 64512
				if view == ViewTable {
					q.PeerID = ""
					q.NeighborAddress = ""
					q.NeighborASN = 0
				}
				result := Execute(context.Background(), c, q)
				if result.Status != "ok" || len(result.Routes) != 1 || !result.Complete {
					t.Fatal("native client failed typed view", view, result.Status, result.ErrorCode)
				}
			}
			q := queryFixture(tc.platform, ViewAccepted)
			q.ExpectedRouterID = "192.0.2.99"
			q.ExpectedRouterASN = 64512
			if r := Execute(context.Background(), c, q); r.Status != "error" || len(r.Routes) != 0 {
				t.Fatal("matching peer on a wrong router socket was accepted")
			}
			mu.Lock()
			defer mu.Unlock()
			for _, command := range commands {
				if command != "restrict" && !strings.HasPrefix(command, "show ") {
					t.Fatal("native worker sent a mutation", command)
				}
			}
		})
	}
}
func TestNativeFRRClientWithMockJSON(t *testing.T) {
	binary := os.Getenv("VTYSH_BIN")
	if binary == "" {
		t.Skip("set VTYSH_BIN for real-client/private-VTY mock")
	}
	dir := t.TempDir()
	// The test package is extracted, not installed. Use a private empty client config.
	currentUser, lookupErr := user.Current()
	if lookupErr != nil || !protocolName.MatchString(currentUser.Username) {
		t.Fatal("test user name unavailable")
	}
	if e := os.WriteFile(filepath.Join(dir, "vtysh.conf"), []byte("username "+currentUser.Username+" nopassword\n"), 0600); e != nil {
		t.Fatal(e)
	}
	clientBinary := executable(t, dir, "vtysh-client", "exec "+quote(binary)+" --config_dir "+quote(dir)+" \"$@\"\n")
	listener, e := net.Listen("unix", filepath.Join(dir, "bgpd.vty"))
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				reader := bufio.NewReader(conn)
				for {
					line, e := reader.ReadString(0)
					if e != nil {
						return
					}
					line = strings.TrimSuffix(line, "\x00")
					reply := ""
					if strings.Contains(line, "summary json") {
						reply = strings.Replace(frrSummaryFixture, `"as":64512`, `"as":64512,"vrfName":"MOEDOVE"`, 1)
					} else if strings.Contains(line, "neighbors 192.0.2.2 routes json") {
						reply = frrRoutesFixture
					}
					conn.Write(append([]byte(reply), 0, 0, 0, 0))
				}
			}()
		}
	}()
	defer func() { listener.Close(); <-done }()
	q := queryFixture("frr", ViewAccepted)
	q.ExpectedRouterID = "192.0.2.1"
	q.ExpectedRouterASN = 64512
	c := Config{RouterID: "router-a", Platform: "frr", Peers: map[string]string{"peer-a": "192.0.2.2"}, Vtysh: clientBinary, VtySocket: dir}
	result := Execute(context.Background(), c, q)
	if result.Status != "ok" || len(result.Routes) != 1 || result.Identity.RouterASN != 64512 {
		t.Fatal("real FRR client/mock query failed", result.Status, result.ErrorCode)
	}
}
func TestNativeOpenBGPDReadOnlyArgumentGrammar(t *testing.T) {
	binary := os.Getenv("BGPCTL_BIN")
	if binary == "" {
		t.Skip("set BGPCTL_BIN for native argument grammar check")
	}
	for _, view := range []string{ViewAccepted, ViewPrePolicy, ViewExported} {
		q := queryFixture("openbgpd", view)
		q.Prefix = "198.51.100.0/24"
		args, e := BuildOpenBGPDRouteCommand(q)
		if e != nil {
			t.Fatal(e)
		}
		args = append([]string{"-s", filepath.Join(t.TempDir(), "absent.sock")}, args...)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		output, e := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		cancel()
		if e == nil || strings.Contains(string(output), "unknown argument") || strings.Contains(string(output), "usage:") || !strings.Contains(string(output), "connect") {
			t.Fatal("native bgpctl did not accept the fixed read-only query grammar", view)
		}
	}
}
