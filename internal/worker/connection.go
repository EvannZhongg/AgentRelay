package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/transport"
	"github.com/gorilla/websocket"
)

var ErrUnauthorized = errors.New("device authorization rejected; use worker setup to pair again")

const (
	pairRetryInitial = 250 * time.Millisecond
	pairRetryMax     = 5 * time.Second
)

func validateBridge(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("bridge must be an absolute HTTPS URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	return errors.New("bridges require HTTPS")
}

func (s *Server) Pair(ctx context.Context, base, fingerprint, statePath string, notify ...func(protocol.PairingRequest)) (protocol.Credentials, error) {
	if err := validateBridge(base); err != nil {
		return protocol.Credentials{}, err
	}
	if fingerprint == "" {
		return protocol.Credentials{}, errors.New("controller certificate fingerprint is required; select a controller discovered on the local network")
	}
	transportConfig := transport.PinnedTLS(fingerprint)
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: transportConfig}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	data, _ := json.Marshal(protocol.PairRequest{Device: s.Device()})
	request, err := submitPairingRequest(ctx, client, base, data)
	if err != nil {
		return protocol.Credentials{}, err
	}
	if len(notify) > 0 && notify[0] != nil {
		notify[0](request)
	}
	var credentials protocol.Credentials
	for {
		if ctx.Err() != nil {
			return credentials, ctx.Err()
		}
		statusResp, err := retryHTTP(ctx, func() (*http.Response, error) {
			statusReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/pair/"+url.PathEscape(request.ID), nil)
			if err != nil {
				return nil, err
			}
			return client.Do(statusReq)
		})
		if err != nil {
			return credentials, err
		}
		if statusResp.StatusCode == http.StatusNotFound {
			statusResp.Body.Close()
			request, err = submitPairingRequest(ctx, client, base, data)
			if err != nil {
				return credentials, err
			}
			if len(notify) > 0 && notify[0] != nil {
				notify[0](request)
			}
			continue
		}
		if statusResp.StatusCode != http.StatusOK {
			err := pairingHTTPError("pairing status request failed", statusResp)
			statusResp.Body.Close()
			return credentials, err
		}
		var status protocol.PairingStatus
		decodeErr := json.NewDecoder(statusResp.Body).Decode(&status)
		statusResp.Body.Close()
		if decodeErr != nil {
			return credentials, fmt.Errorf("decode pairing status: %w", decodeErr)
		}
		if status.Status == "approved" && status.Credentials != nil {
			credentials = *status.Credentials
			credentials.BridgeURL = strings.TrimRight(base, "/")
			credentials.Workspace = s.cfg.Workspace
			if credentials.DeviceID == "" || credentials.Token == "" || credentials.Fingerprint == "" {
				return credentials, errors.New("invalid pairing approval")
			}
			break
		}
		if status.Status == "rejected" {
			return credentials, errors.New("pairing request was rejected by the controller")
		}
		if status.Status != "pending" {
			return credentials, fmt.Errorf("pairing request failed: %s", status.Status)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return credentials, ctx.Err()
		case <-timer.C:
		}
	}
	if err := state.Save(statePath, credentials); err != nil {
		return credentials, fmt.Errorf("save worker credentials: %w", err)
	}
	s.cfg.DeviceID = credentials.DeviceID
	return credentials, nil
}

func submitPairingRequest(ctx context.Context, client *http.Client, base string, data []byte) (protocol.PairingRequest, error) {
	resp, err := retryHTTP(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/pair", bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return client.Do(req)
	})
	if err != nil {
		return protocol.PairingRequest{}, err
	}
	if resp.StatusCode != http.StatusAccepted {
		err := pairingHTTPError("pairing request failed", resp)
		resp.Body.Close()
		return protocol.PairingRequest{}, err
	}
	var request protocol.PairingRequest
	err = json.NewDecoder(resp.Body).Decode(&request)
	resp.Body.Close()
	if err != nil {
		return protocol.PairingRequest{}, fmt.Errorf("decode pairing request: %w", err)
	}
	if request.ID == "" || request.Verification == "" || request.ExpiresAt.Before(time.Now()) {
		return protocol.PairingRequest{}, errors.New("invalid pairing request response")
	}
	if request.Fingerprint == "" {
		return protocol.PairingRequest{}, errors.New("controller did not provide a certificate identity")
	}
	return request, nil
}

func retryHTTP(ctx context.Context, operation func() (*http.Response, error)) (*http.Response, error) {
	delay := pairRetryInitial
	for {
		resp, err := operation()
		if err == nil {
			return resp, nil
		}
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if delay < pairRetryMax {
			delay *= 2
			if delay > pairRetryMax {
				delay = pairRetryMax
			}
		}
	}
}

func pairingHTTPError(prefix string, resp *http.Response) error {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("%s (%s): %w", prefix, resp.Status, readErr)
	}
	var failure protocol.ErrorResponse
	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
		message = failure.Error
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%s (%s): %s", prefix, resp.Status, message)
}

func (s *Server) Connect(ctx context.Context, credentials protocol.Credentials) (*transport.Peer, error) {
	if err := validateBridge(credentials.BridgeURL); err != nil {
		return nil, err
	}
	if s.cfg.Workspace != credentials.Workspace {
		return nil, errors.New("workspace changed; pair again to authorize it")
	}
	u, _ := url.Parse(credentials.BridgeURL)
	u.Scheme = "wss"
	u.Path = "/v1/connect"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+credentials.Token)
	headers.Set("X-AgentRelay-Device", credentials.DeviceID)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, TLSClientConfig: transport.PinnedTLS(credentials.Fingerprint)}
	conn, resp, err := dialer.DialContext(ctx, u.String(), headers)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return nil, ErrUnauthorized
			}
		}
		return nil, err
	}
	peer := transport.NewPeer(conn, transport.HTTPHandler(s.Handler()))
	device := s.Device()
	device.ID = credentials.DeviceID
	data, _ := json.Marshal(device)
	registrationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := peer.Call(registrationCtx, transport.Request{Method: "POST", Path: "/register", Body: data})
	if err != nil || result.Status != 200 {
		peer.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("device registration rejected: %d", result.Status)
	}
	return peer, nil
}

func (s *Server) Run(ctx context.Context, credentials protocol.Credentials, report func(error)) error {
	delay := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		peer, err := s.Connect(ctx, credentials)
		if errors.Is(err, ErrUnauthorized) {
			return err
		}
		if err == nil {
			delay = time.Second
			select {
			case <-ctx.Done():
				peer.Close()
				return ctx.Err()
			case <-peer.Done():
			}
			err = errors.New("bridge connection lost; reconnecting")
		}
		if report != nil {
			report(err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}
