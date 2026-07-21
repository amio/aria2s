package aria2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
)

var ErrTransportUnavailable = errors.New("aria2 rpc transport unavailable")
var ErrOutcomeUnknown = errors.New("aria2 mutation outcome unknown")

/** RPCError is a confirmed JSON-RPC rejection. */
type RPCError struct {
	Method  string
	Code    int
	Message string
}

func (err *RPCError) Error() string {
	return fmt.Sprintf("%s failed (%d): %s", err.Method, err.Code, err.Message)
}

/** OutcomeUnknownError marks a dispatched mutation whose server outcome was not confirmed. */
type OutcomeUnknownError struct {
	Method string
	Cause  error
}

func (err *OutcomeUnknownError) Error() string {
	return fmt.Sprintf("%s: %v", ErrOutcomeUnknown, err.Cause)
}

func (err *OutcomeUnknownError) Unwrap() []error { return []error{ErrOutcomeUnknown, err.Cause} }

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

/** AddOptions carries optional per-task overrides sent to aria2.addUri. */
type AddOptions struct {
	Dir string
}

func (client *RPCClient) AddURI(ctx context.Context, uri string, opts AddOptions) (string, error) {
	if !isSupportedURI(uri) {
		return "", fmt.Errorf("unsupported URI: %s", uri)
	}
	params := []any{[]string{uri}}
	if opts.Dir != "" {
		params = append(params, map[string]string{"dir": opts.Dir})
	}
	var gid string
	if err := client.mutation(ctx, "aria2.addUri", params, &gid); err != nil {
		return "", err
	}
	return gid, nil
}

// ChangeOption applies per-download option overrides (aria2.changeOption).
// Active downloads accept only a subset of options; dir is accepted while
// paused, which is how the staging hook repoints seeding tasks after a move.
func (client *RPCClient) ChangeOption(ctx context.Context, gid string, options map[string]string) error {
	var ignored string
	return client.mutation(ctx, "aria2.changeOption", []any{gid, options}, &ignored)
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
	return client.doCall(ctx, method, params, result, false)
}

func (client *RPCClient) mutation(ctx context.Context, method string, params []any, result any) error {
	return client.doCall(ctx, method, params, result, true)
}

func (client *RPCClient) doCall(ctx context.Context, method string, params []any, result any, mutation bool) error {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  method,
		Params:  append([]any{"token:" + client.secret}, params...),
	}
	return client.dispatch(ctx, method, payload, result, mutation)
}

func (client *RPCClient) dispatch(ctx context.Context, method string, payload rpcRequest, result any, mutation bool) error {
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
		return classifyDispatched(method, err, mutation)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyDispatched(method, fmt.Errorf("%w: aria2 RPC returned HTTP %d", ErrTransportUnavailable, resp.StatusCode), mutation)
	}
	var decoded rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return classifyDispatched(method, err, mutation)
	}
	if decoded.Error != nil {
		return &RPCError{Method: method, Code: decoded.Error.Code, Message: decoded.Error.Message}
	}
	if result == nil {
		return nil
	}
	if len(decoded.Result) == 0 || bytes.Equal(decoded.Result, []byte("null")) {
		return classifyDispatched(method, errors.New("missing RPC result"), mutation)
	}
	if err := json.Unmarshal(decoded.Result, result); err != nil {
		return classifyDispatched(method, err, mutation)
	}
	return nil
}

func classifyDispatched(method string, err error, mutation bool) error {
	err = WrapTransportError(err)
	if mutation {
		return &OutcomeUnknownError{Method: method, Cause: err}
	}
	return err
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
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func isSupportedURI(uri string) bool {
	return strings.HasPrefix(uri, "http://") ||
		strings.HasPrefix(uri, "https://") ||
		strings.HasPrefix(uri, "magnet:")
}

func WrapTransportError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *neturl.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("%w: %v", ErrTransportUnavailable, err)
	}
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %v", ErrTransportUnavailable, err)
	}
	return err
}
