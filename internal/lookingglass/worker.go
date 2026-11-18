package lookingglass

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

func workerEndpoint(endpoint, path string) (string, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/" && u.Path != "/api/monitoring/ingest") {
		return "", errors.New("looking-glass worker requires the trusted HTTPS control-plane origin")
	}
	u.Path = path
	u.RawPath = ""
	return u.String(), nil
}
func workerPost(ctx context.Context, client *http.Client, endpoint, token string, body any, result any) error {
	if !safeToken(token) {
		return errors.New("invalid worker token")
	}
	raw, e := json.Marshal(body)
	if e != nil || len(raw) > MaxOutputBytes {
		return errors.New("bounded worker payload could not be encoded")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if e != nil {
		return errors.New("could not construct worker request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if client == nil {
		client = &http.Client{}
	}
	safe := *client
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, e := safe.Do(req)
	if e != nil {
		return errors.New("looking-glass HTTPS request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("looking-glass endpoint returned HTTP %d", response.StatusCode)
	}
	d := json.NewDecoder(io.LimitReader(response.Body, 65537))
	d.DisallowUnknownFields()
	if d.Decode(result) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid bounded worker response")
	}
	return nil
}
func Claim(ctx context.Context, client *http.Client, endpoint, token, routerID string) (*Lease, error) {
	if !identifier.MatchString(routerID) {
		return nil, errors.New("invalid worker router identity")
	}
	path, e := workerEndpoint(endpoint, "/api/looking-glass/claim")
	if e != nil {
		return nil, e
	}
	var reply ClaimResponse
	if e = workerPost(ctx, client, path, token, ClaimRequest{routerID}, &reply); e != nil {
		return nil, e
	}
	if reply.Lease == nil {
		return nil, nil
	}
	lease := reply.Lease
	normalized, e := NormalizeQuery(lease.Query)
	if e != nil || normalized != lease.Query || lease.Query.RouterID != routerID || !identifier.MatchString(lease.ID) || !safeToken(lease.Token) || !time.Now().Before(lease.ExpiresAt) {
		return nil, errors.New("lease is expired or does not match this configured router")
	}
	return lease, nil
}
func Complete(ctx context.Context, client *http.Client, endpoint, token string, lease Lease, result Result) error {
	if e := ValidateResult(result, lease.Query); e != nil {
		return errors.New("invalid typed looking-glass result")
	}
	if !identifier.MatchString(lease.ID) || !safeToken(lease.Token) {
		return errors.New("invalid lease completion identity")
	}
	path, e := workerEndpoint(endpoint, "/api/looking-glass/complete")
	if e != nil {
		return e
	}
	var ack struct {
		OK        bool `json:"ok"`
		Duplicate bool `json:"duplicate"`
	}
	if e = workerPost(ctx, client, path, token, Completion{lease.ID, lease.Token, result}, &ack); e != nil {
		return e
	}
	if !ack.OK {
		return errors.New("looking-glass completion was not acknowledged")
	}
	return nil
}
func RunOnce(ctx context.Context, client *http.Client, endpoint, token string, config Config) (bool, error) {
	if e := ValidateConfig(config); e != nil {
		return false, e
	}
	lease, e := Claim(ctx, client, endpoint, token, config.RouterID)
	if e != nil || lease == nil {
		return false, e
	}
	queryCtx, cancel := context.WithDeadline(ctx, lease.ExpiresAt)
	result := Execute(queryCtx, config, lease.Query)
	cancel()
	if e = Complete(ctx, client, endpoint, token, *lease, result); e != nil {
		return true, e
	}
	return true, nil
}
