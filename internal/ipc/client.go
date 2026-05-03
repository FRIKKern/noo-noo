package ipc

import (
	"fmt"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
)

// Client is a typed wrapper around a JSON-RPC client over a Unix socket.
type Client struct {
	rpc *rpc.Client
}

// Dial connects to the daemon socket.
func Dial(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial unix %s: %w", socketPath, err)
	}
	return &Client{rpc: jsonrpc.NewClient(conn)}, nil
}

// Close releases the underlying RPC connection.
func (c *Client) Close() error { return c.rpc.Close() }

// DaemonStatus calls Daemon.Status.
func (c *Client) DaemonStatus() (StatusResponse, error) {
	var resp StatusResponse
	err := c.rpc.Call("Daemon.Status", StatusRequest{}, &resp)
	return resp, err
}

// ReportFull calls Report.Full.
func (c *Client) ReportFull() (Report, error) {
	var resp Report
	err := c.rpc.Call("Report.Full", ReportRequest{}, &resp)
	return resp, err
}

// SuggestionsList calls Suggestions.List and returns just the items.
func (c *Client) SuggestionsList() ([]SuggestionAlias, error) {
	var resp SuggestionsResponse
	if err := c.rpc.Call("Suggestions.List", SuggestionsRequest{}, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// SuggestionsDismiss calls Suggestions.Dismiss for the given suggestion id.
func (c *Client) SuggestionsDismiss(id int64) error {
	var resp DismissResponse
	return c.rpc.Call("Suggestions.Dismiss", DismissRequest{ID: id}, &resp)
}

// TriggerScan calls Daemon.TriggerScan to request an immediate heuristic
// scan instead of waiting for the daily scheduler tick.
func (c *Client) TriggerScan() (TriggerScanReply, error) {
	var resp TriggerScanReply
	err := c.rpc.Call("Daemon.TriggerScan", TriggerScanArgs{}, &resp)
	return resp, err
}

// CleanExecute calls Clean.Execute with the supplied targets.
func (c *Client) CleanExecute(targets []CleanTarget) (CleanResponse, error) {
	var resp CleanResponse
	err := c.rpc.Call("Clean.Execute", CleanRequest{Targets: targets}, &resp)
	return resp, err
}
