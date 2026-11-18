package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const quaggaRequestLimit = 2*maxConfigSize + (64 << 10)

type quaggaSocketReply struct {
	Transaction QuaggaTransaction `json:"transaction"`
	Error       string            `json:"error,omitempty"`
}

type quaggaSocketWatchdog interface {
	Status() QuaggaTransaction
	Stage(context.Context, QuaggaStageRequest) (QuaggaTransaction, error)
	Confirm(context.Context, QuaggaDecision) (QuaggaTransaction, error)
	Cancel(QuaggaDecision) (QuaggaTransaction, error)
	Tick() error
	Close() error
}

// ServeQuaggaWatchdog starts one dedicated Quagga instance and accepts only
// typed transactions on its private, same-UID control socket.
func ServeQuaggaWatchdog(ctx context.Context, opts QuaggaOptions, socket string) error {
	if err := checkQuaggaSocketPath(socket); err != nil {
		return err
	}
	if err := quaggaOptionsValid(opts); err != nil {
		return err
	}
	if filepath.Dir(socket) == opts.StateDir {
		switch filepath.Base(socket) {
		case "bgpd.vty", "no-zebra.sock", "quagga", "quagga.moedove-lock", "transaction.json", "process.json", "policy-0.conf", "policy-1.conf":
			return errors.New("Quagga control socket collides with a watchdog state path")
		}
	}
	if err := secureDir(filepath.Dir(socket)); err != nil {
		return err
	}
	return serveQuaggaSocket(ctx, socket, func() (quaggaSocketWatchdog, error) {
		return OpenQuaggaWatchdog(ctx, opts)
	})
}

func checkQuaggaSocketPath(socket string) error {
	if !safePath.MatchString(socket) || filepath.Clean(socket) != socket || len(socket) >= len(syscall.RawSockaddrUnix{}.Path) {
		return errors.New("Quagga control socket requires an absolute clean path within the Linux Unix socket limit")
	}
	return nil
}

func checkQuaggaSocketDirectory(socket string) error {
	path := filepath.Dir(socket)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("Quagga control socket directory requires an existing non-symlink path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("Quagga control socket directory requires mode 0700")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("Quagga control socket directory requires the watchdog UID")
	}
	return nil
}

func quaggaControlSocketInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errors.New("Quagga control path requires an owned single-link Unix socket with mode 0600")
	}
	return info, nil
}

func quaggaRemoveSocket(path string, previous os.FileInfo) error {
	current, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !os.SameFile(previous, current) {
		return errors.New("Quagga control socket identity changed")
	}
	return os.Remove(path)
}

func quaggaWatchListener(socket string) (*net.UnixListener, os.FileInfo, error) {
	if info, err := quaggaControlSocketInfo(socket); err == nil {
		conn, dialErr := net.DialTimeout("unix", socket, 300*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil, nil, errors.New("another process already owns the Quagga control socket")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, nil, errors.New("Quagga control socket ownership could not be verified")
		}
		if err = quaggaRemoveSocket(socket, info); err != nil {
			return nil, nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, nil, err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		listener.Close()
		return nil, nil, err
	}
	info, err := quaggaControlSocketInfo(socket)
	if err != nil {
		listener.Close()
		return nil, nil, err
	}
	listener.SetUnlinkOnClose(false)
	return listener, info, nil
}

type quaggaUIDListener struct{ *net.UnixListener }

func (l quaggaUIDListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if quaggaWatchdogPeerUID(conn) {
			return conn, nil
		}
		conn.Close()
	}
}

func quaggaWatchdogPeerUID(conn *net.UnixConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var inspect error
	err = raw.Control(func(fd uintptr) {
		cred, inspect = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return err == nil && inspect == nil && cred != nil && cred.Pid > 0 && cred.Uid == uint32(os.Geteuid())
}

func serveQuaggaSocket(ctx context.Context, socket string, open func() (quaggaSocketWatchdog, error)) (result error) {
	if err := checkQuaggaSocketDirectory(socket); err != nil {
		return err
	}
	unlock, err := deploymentLock(socket)
	if err != nil {
		return err
	}
	defer unlock()
	listener, info, err := quaggaWatchListener(socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		result = errors.Join(result, quaggaRemoveSocket(socket, info))
	}()
	watchdog, err := open()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, watchdog.Close()) }()
	server := &http.Server{Handler: quaggaWatchdogHandler(ctx, watchdog), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(quaggaUIDListener{listener}) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			if err := watchdog.Tick(); err != nil {
				return err
			}
		}
	}
}

func quaggaWatchdogHandler(ctx context.Context, w quaggaSocketWatchdog) http.HandlerFunc {
	return func(out http.ResponseWriter, r *http.Request) {
		if r.URL.ForceQuery || r.URL.RawQuery != "" || r.URL.RawPath != "" {
			writeQuaggaReply(out, http.StatusBadRequest, QuaggaTransaction{}, errors.New("invalid Quagga request path"))
			return
		}
		if r.URL.Path == "/status" && r.Method == http.MethodGet {
			writeQuaggaReply(out, http.StatusOK, w.Status(), nil)
			return
		}
		if r.Method != http.MethodPost || (r.URL.Path != "/stage" && r.URL.Path != "/confirm" && r.URL.Path != "/cancel") {
			writeQuaggaReply(out, http.StatusMethodNotAllowed, QuaggaTransaction{}, errors.New("unsupported Quagga request"))
			return
		}
		var state QuaggaTransaction
		var err error
		// Once decoded, the watchdog journal and service context own the action.
		// A client disconnect cannot cancel a partially applied replacement.
		if r.URL.Path == "/stage" {
			var request QuaggaStageRequest
			if !decodeQuaggaRequest(out, r, quaggaRequestLimit, map[string]any{"config": &request.Config, "checksum": &request.Checksum, "baseChecksum": &request.BaseChecksum, "timeoutSeconds": &request.TimeoutSeconds}) {
				return
			}
			state, err = w.Stage(ctx, request)
		} else {
			var decision QuaggaDecision
			if !decodeQuaggaRequest(out, r, 1024, map[string]any{"id": &decision.ID, "checksum": &decision.Checksum}) {
				return
			}
			if r.URL.Path == "/confirm" {
				state, err = w.Confirm(ctx, decision)
			} else {
				state, err = w.Cancel(decision)
			}
		}
		code := http.StatusOK
		if err != nil {
			code = http.StatusConflict
		}
		writeQuaggaReply(out, code, state, err)
	}
}

func decodeQuaggaRequest(out http.ResponseWriter, r *http.Request, limit int64, fields map[string]any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(out, r.Body, limit))
	if err := decodeExactQuaggaObject(dec, fields); err != nil {
		writeQuaggaReply(out, http.StatusBadRequest, QuaggaTransaction{}, err)
		return false
	}
	return true
}

func decodeExactQuaggaObject(dec *json.Decoder, fields map[string]any) error {
	invalid := errors.New("invalid Quagga transaction JSON")
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return invalid
	}
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		value, present := fields[name]
		if err != nil || !ok || !present {
			return invalid
		}
		token, err := dec.Token()
		if err != nil {
			return invalid
		}
		switch dst := value.(type) {
		case *string:
			v, ok := token.(string)
			if !ok {
				return invalid
			}
			*dst = v
		case *int:
			v, ok := token.(json.Number)
			if !ok {
				return invalid
			}
			n, err := strconv.Atoi(string(v))
			if err != nil {
				return invalid
			}
			*dst = n
		default:
			return invalid
		}
		delete(fields, name)
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') || len(fields) != 0 || dec.Decode(new(any)) != io.EOF {
		return invalid
	}
	return nil
}

func writeQuaggaReply(out http.ResponseWriter, status int, state QuaggaTransaction, err error) {
	reply := quaggaSocketReply{Transaction: state}
	if err != nil {
		reply.Error = err.Error()
	}
	out.Header().Set("Content-Type", "application/json")
	out.Header().Set("Cache-Control", "no-store")
	out.WriteHeader(status)
	_ = json.NewEncoder(out).Encode(reply)
}

// ReadQuaggaArtifact applies the watchdog's private-file contract to a staged
// artifact before it crosses the local control socket.
func ReadQuaggaArtifact(path string) (string, error) {
	data, err := quaggaPrivateRead(path, maxConfigSize)
	return string(data), err
}

func RequestQuagga(ctx context.Context, socket, action string, payload any) (QuaggaTransaction, error) {
	method := http.MethodPost
	switch action {
	case "status":
		method = http.MethodGet
		if payload != nil {
			return QuaggaTransaction{}, errors.New("Quagga status requires no payload")
		}
	case "stage":
		if p, ok := payload.(QuaggaStageRequest); !ok || len(p.Config) > maxConfigSize {
			return QuaggaTransaction{}, errors.New("Quagga stage requires a bounded typed configuration")
		}
	case "confirm", "cancel":
		if _, ok := payload.(QuaggaDecision); !ok {
			return QuaggaTransaction{}, errors.New("Quagga confirm/cancel requires a typed decision")
		}
	default:
		return QuaggaTransaction{}, errors.New("unsupported Quagga action")
	}
	if err := checkQuaggaSocketPath(socket); err != nil {
		return QuaggaTransaction{}, err
	}
	if err := checkQuaggaSocketDirectory(socket); err != nil {
		return QuaggaTransaction{}, err
	}
	info, err := quaggaControlSocketInfo(socket)
	if err != nil {
		return QuaggaTransaction{}, err
	}
	var body io.Reader
	if payload != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil || buf.Len() > quaggaRequestLimit {
			return QuaggaTransaction{}, errors.New("could not encode a bounded Quagga request")
		}
		body = &buf
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, err
		}
		current, err := quaggaControlSocketInfo(socket)
		if err != nil || !os.SameFile(info, current) || !quaggaWatchdogPeerUID(conn.(*net.UnixConn)) {
			conn.Close()
			return nil, errors.New("Quagga control socket identity changed")
		}
		return conn, nil
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, method, "http://localhost/"+action, body)
	if err != nil {
		return QuaggaTransaction{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return QuaggaTransaction{}, fmt.Errorf("Quagga request failed; inspect status before retrying: %w", err)
	}
	defer response.Body.Close()
	const replyLimit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(response.Body, replyLimit+1))
	if err != nil || len(raw) > replyLimit {
		return QuaggaTransaction{}, errors.New("Quagga response unavailable or exceeded its bound")
	}
	var reply quaggaSocketReply
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&reply); err != nil || dec.Decode(new(any)) != io.EOF {
		return QuaggaTransaction{}, errors.New("invalid Quagga control response")
	}
	if response.StatusCode != http.StatusOK || reply.Error != "" {
		return reply.Transaction, fmt.Errorf("Quagga transaction refused: %s", reply.Error)
	}
	if reply.Transaction.Version != 1 || !quaggaChecksum(reply.Transaction.ActiveChecksum) {
		return QuaggaTransaction{}, errors.New("Quagga control response omitted the active transaction state")
	}
	return reply.Transaction, nil
}
