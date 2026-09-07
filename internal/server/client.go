package server

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Call performs one socket API call and returns the data payload.
// A server-side failure is a Go error (Response.Error).
func Call(sock, op string, args any) (json.RawMessage, error) {
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("api %s: dial %s: %w", op, sock, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	var rawArgs json.RawMessage
	if args != nil {
		rawArgs, err = json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("api %s: encode args: %w", op, err)
		}
	}
	req, err := json.Marshal(Request{Op: op, Args: rawArgs})
	if err != nil {
		return nil, fmt.Errorf("api %s: encode request: %w", op, err)
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return nil, fmt.Errorf("api %s: write: %w", op, err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("api %s: decode response: %w", op, err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("api %s: %s", op, resp.Error)
	}
	return resp.Data, nil
}
