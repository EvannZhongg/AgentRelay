package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (s *Server) Pair(ctx context.Context, base, code, statePath string) (protocol.Credentials, error) {
	if err := validateBridge(base); err != nil {
		return protocol.Credentials{}, err
	}
	parts := strings.Split(code, ".")
	if len(parts) != 2 || len(parts[0]) != 64 || len(parts[1]) != 64 {
		return protocol.Credentials{}, errors.New("paste the full pairing code from the controller")
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: transport.PinnedTLS(parts[0])}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var credentials protocol.Credentials
	data, _ := json.Marshal(protocol.PairRequest{Code: code, Device: s.Device()})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/v1/pair", bytes.NewReader(data))
	if err != nil {
		return credentials, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return credentials, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		var failure protocol.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		return credentials, fmt.Errorf("pairing failed (%d): %s", resp.StatusCode, failure.Error)
	}
	if err := json.NewDecoder(resp.Body).Decode(&credentials); err != nil {
		return credentials, err
	}
	credentials.BridgeURL = strings.TrimRight(base, "/")
	credentials.Fingerprint = parts[0]
	credentials.Workspace = s.cfg.Workspace
	if credentials.DeviceID == "" || credentials.Token == "" {
		return credentials, errors.New("invalid pairing response")
	}
	if err := state.Save(statePath, credentials); err != nil {
		return credentials, fmt.Errorf("save worker credentials: %w", err)
	}
	s.cfg.DeviceID = credentials.DeviceID
	return credentials, nil
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
