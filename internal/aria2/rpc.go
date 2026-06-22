package aria2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type RPCClient struct {
	endpoint string
	secret   string
	client   *http.Client
}

func NewRPCClient(endpoint, secret string, client *http.Client) *RPCClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &RPCClient{endpoint: endpoint, secret: secret, client: client}
}

func (client *RPCClient) AddURI(ctx context.Context, uri string) (string, error) {
	if !isSupportedURI(uri) {
		return "", fmt.Errorf("unsupported URI: %s", uri)
	}
	var gid string
	if err := client.call(ctx, "aria2.addUri", []any{[]string{uri}}, &gid); err != nil {
		return "", err
	}
	return gid, nil
}

func (client *RPCClient) Version(ctx context.Context) (string, error) {
	var version struct {
		Version string `json:"version"`
	}
	if err := client.call(ctx, "aria2.getVersion", nil, &version); err != nil {
		return "", err
	}
	return version.Version, nil
}

func (client *RPCClient) call(ctx context.Context, method string, params []any, result any) error {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  method,
		Params:  append([]any{"token:" + client.secret}, params...),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("aria2 RPC returned HTTP %d", resp.StatusCode)
	}
	var decoded rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return err
	}
	if decoded.Error != nil {
		return errors.New(decoded.Error.Message)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(decoded.Result, result)
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Message string `json:"message"`
}

func isSupportedURI(uri string) bool {
	return strings.HasPrefix(uri, "http://") ||
		strings.HasPrefix(uri, "https://") ||
		strings.HasPrefix(uri, "magnet:")
}
