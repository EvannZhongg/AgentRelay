package transport

import (
	"github.com/gorilla/websocket"
	"net/http"
	"time"
)

// AcceptWorker upgrades an already authenticated connection. Device identity,
// credential authorization and registration remain owned by the Controller.
func AcceptWorker(w http.ResponseWriter, r *http.Request, handler Handler) (*Peer, error) {
	conn, err := (&websocket.Upgrader{HandshakeTimeout: 10 * time.Second}).Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	return NewPeer(conn, handler), nil
}
