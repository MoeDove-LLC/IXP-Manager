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
	"time"
)

type OpenBGPDResponse struct {
	Transaction OpenBGPDTransaction `json:"transaction"`
	Error       string              `json:"error,omitempty"`
}

// ServeOpenBGPDWatchdog exposes only a private local Unix socket. There is no TCP
// listener, remote credential, arbitrary command, or caller-selected target.
func ServeOpenBGPDWatchdog(ctx context.Context, opts OpenBGPDOptions, socket string) (result error) {
	return serveOpenBGPDWatchdog(ctx, opts, socket, OpenOpenBGPDWatchdog)
}
func serveOpenBGPDWatchdog(ctx context.Context, opts OpenBGPDOptions, socket string, open func(context.Context, OpenBGPDOptions) (*OpenBGPDWatchdog, error)) (result error) {
	if !safePath.MatchString(socket) || filepath.Clean(socket) != socket {
		return errors.New("agent socket must be an absolute clean path")
	}
	if err := secureDir(filepath.Dir(socket)); err != nil {
		return err
	}
	w, err := open(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, w.Close()) }()
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("agent socket path is not a socket")
		}
		conn, err := net.DialTimeout("unix", socket, 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return errors.New("another agent already owns this socket")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0600); err != nil {
		listener.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(out http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			out.WriteHeader(405)
			return
		}
		writeOpenBGPDResponse(out, 200, w.Status(), nil)
	})
	mux.HandleFunc("/stage", func(out http.ResponseWriter, r *http.Request) {
		var request OpenBGPDStageRequest
		if !decodeOpenBGPDRequest(out, r, &request) {
			return
		}
		// Once accepted, HTTP disconnect does not cancel a partially applied change.
		// The service deadline and persisted journal own its completion/recovery.
		state, err := w.Stage(ctx, request)
		code := 200
		if err != nil {
			code = 409
		}
		writeOpenBGPDResponse(out, code, state, err)
	})
	for _, path := range []string{"/confirm", "/cancel"} {
		path := path
		mux.HandleFunc(path, func(out http.ResponseWriter, r *http.Request) {
			var decision OpenBGPDDecision
			if !decodeOpenBGPDRequest(out, r, &decision) {
				return
			}
			var state OpenBGPDTransaction
			var err error
			if path == "/confirm" {
				state, err = w.Confirm(ctx, decision)
			} else {
				state, err = w.Cancel(decision)
			}
			code := 200
			if err != nil {
				code = 409
			}
			writeOpenBGPDResponse(out, code, state, err)
		})
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Closing listeners and active request transports stops new submissions;
			// w.Close waits for serialized stage work and restores anything unconfirmed.
			_ = server.Close()
			return w.Close()
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			if err := w.Tick(); err != nil {
				// Preserve the journal and let the external supervisor reopen the
				// verified parent identity (including after a daemon restart).
				_ = server.Close()
				return err
			}
		}
	}
}
func decodeOpenBGPDRequest(out http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != "POST" {
		out.WriteHeader(405)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(out, r.Body, maxConfigSize+65536))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeOpenBGPDResponse(out, 400, OpenBGPDTransaction{}, errors.New("invalid transaction request"))
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		writeOpenBGPDResponse(out, 400, OpenBGPDTransaction{}, errors.New("one JSON request required"))
		return false
	}
	return true
}
func writeOpenBGPDResponse(out http.ResponseWriter, code int, state OpenBGPDTransaction, err error) {
	out.Header().Set("Content-Type", "application/json")
	out.Header().Set("Cache-Control", "no-store")
	out.WriteHeader(code)
	response := OpenBGPDResponse{Transaction: state}
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(out).Encode(response)
}

func RequestOpenBGPD(ctx context.Context, socket, action string, payload any) (OpenBGPDTransaction, error) {
	if action != "status" && action != "stage" && action != "confirm" && action != "cancel" {
		return OpenBGPDTransaction{}, errors.New("unsupported transaction action")
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return OpenBGPDTransaction{}, err
		}
		body = bytes.NewReader(b)
	}
	method := "POST"
	if action == "status" {
		method = "GET"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 120 * time.Second}
	request, err := http.NewRequestWithContext(ctx, method, "http://localhost/"+action, body)
	if err != nil {
		return OpenBGPDTransaction{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return OpenBGPDTransaction{}, fmt.Errorf("agent request failed; inspect status before retrying: %w", err)
	}
	defer response.Body.Close()
	var reply OpenBGPDResponse
	dec := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err = dec.Decode(&reply); err != nil {
		return OpenBGPDTransaction{}, err
	}
	if response.StatusCode != 200 || reply.Error != "" {
		return reply.Transaction, fmt.Errorf("OpenBGPD transaction refused: %s", reply.Error)
	}
	return reply.Transaction, nil
}
