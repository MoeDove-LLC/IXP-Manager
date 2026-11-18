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

type FRRResponse struct {
	Transaction FRRTransaction `json:"transaction"`
	Error       string         `json:"error,omitempty"`
}

// ServeFRRWatchdog exposes only a private local Unix socket. There is no TCP
// listener, remote credential, arbitrary command, or caller-selected target.
func ServeFRRWatchdog(ctx context.Context, opts FRROptions, socket string) (result error) {
	if !safePath.MatchString(socket) || filepath.Clean(socket) != socket {
		return errors.New("agent socket must be an absolute clean path")
	}
	if err := secureDir(filepath.Dir(socket)); err != nil {
		return err
	}
	w, err := OpenFRRWatchdog(ctx, opts)
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
		writeFRRResponse(out, 200, w.Status(), nil)
	})
	mux.HandleFunc("/stage", func(out http.ResponseWriter, r *http.Request) {
		var request FRRStageRequest
		if !decodeFRRRequest(out, r, &request) {
			return
		}
		// Once accepted, HTTP disconnect does not cancel a partially applied change.
		// The service deadline and persisted journal own its completion/recovery.
		state, err := w.Stage(ctx, request)
		code := 200
		if err != nil {
			code = 409
		}
		writeFRRResponse(out, code, state, err)
	})
	for _, path := range []string{"/confirm", "/cancel"} {
		path := path
		mux.HandleFunc(path, func(out http.ResponseWriter, r *http.Request) {
			var decision FRRDecision
			if !decodeFRRRequest(out, r, &decision) {
				return
			}
			var state FRRTransaction
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
			writeFRRResponse(out, code, state, err)
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
				fmt.Fprintln(os.Stderr, err)
			}
		}
	}
}
func decodeFRRRequest(out http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != "POST" {
		out.WriteHeader(405)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(out, r.Body, maxConfigSize+65536))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeFRRResponse(out, 400, FRRTransaction{}, errors.New("invalid transaction request"))
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		writeFRRResponse(out, 400, FRRTransaction{}, errors.New("one JSON request required"))
		return false
	}
	return true
}
func writeFRRResponse(out http.ResponseWriter, code int, state FRRTransaction, err error) {
	out.Header().Set("Content-Type", "application/json")
	out.Header().Set("Cache-Control", "no-store")
	out.WriteHeader(code)
	response := FRRResponse{Transaction: state}
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(out).Encode(response)
}

func RequestFRR(ctx context.Context, socket, action string, payload any) (FRRTransaction, error) {
	if action != "status" && action != "stage" && action != "confirm" && action != "cancel" {
		return FRRTransaction{}, errors.New("unsupported transaction action")
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return FRRTransaction{}, err
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
		return FRRTransaction{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return FRRTransaction{}, fmt.Errorf("agent request failed; inspect status before retrying: %w", err)
	}
	defer response.Body.Close()
	var reply FRRResponse
	dec := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err = dec.Decode(&reply); err != nil {
		return FRRTransaction{}, err
	}
	if response.StatusCode != 200 || reply.Error != "" {
		return reply.Transaction, fmt.Errorf("FRR transaction refused: %s", reply.Error)
	}
	return reply.Transaction, nil
}
