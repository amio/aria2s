package aria2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type multicallRequest struct {
	MethodName string `json:"methodName"`
	Params     []any  `json:"params"`
}

type multicallFault struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type nestedRPCError struct {
	Method string
	Index  int
	Err    error
}

func (err *nestedRPCError) Error() string {
	return fmt.Sprintf("multicall[%d] %s: %v", err.Index, err.Method, err.Err)
}

func (err *nestedRPCError) Unwrap() error { return err.Err }

type callDescriptor struct {
	method string
	params []any
	apply  func(json.RawMessage) error
}

// multicall owns aria2's unusual authentication and nested result shape so callers
// cannot accidentally put the token on the outer system.multicall request.
func (client *RPCClient) multicall(ctx context.Context, descriptors []callDescriptor) []error {
	calls := make([]multicallRequest, len(descriptors))
	for index, descriptor := range descriptors {
		calls[index] = multicallRequest{
			MethodName: descriptor.method,
			Params:     append([]any{"token:" + client.secret}, descriptor.params...),
		}
	}
	var results []json.RawMessage
	if err := client.callWithoutToken(ctx, "system.multicall", []any{calls}, &results); err != nil {
		return []error{err}
	}
	if len(results) != len(descriptors) {
		return []error{fmt.Errorf("malformed multicall: got %d results for %d calls", len(results), len(descriptors))}
	}
	errs := make([]error, len(descriptors))
	for index, raw := range results {
		var success []json.RawMessage
		if err := json.Unmarshal(raw, &success); err == nil {
			if len(success) != 1 {
				return []error{fmt.Errorf("malformed multicall result %d: expected one-item array", index)}
			}
			if err := descriptors[index].apply(success[0]); err != nil {
				return []error{fmt.Errorf("malformed multicall result %d: %w", index, err)}
			}
			continue
		}
		var fault multicallFault
		if err := json.Unmarshal(raw, &fault); err != nil || fault.Message == "" {
			return []error{fmt.Errorf("malformed multicall result %d", index)}
		}
		rpcErr := &RPCError{Method: descriptors[index].method, Code: fault.Code, Message: fault.Message}
		errs[index] = &nestedRPCError{Method: descriptors[index].method, Index: index, Err: rpcErr}
	}
	return errs
}

func (client *RPCClient) callWithoutToken(ctx context.Context, method string, params []any, result any) error {
	payload := rpcRequest{JSONRPC: "2.0", ID: "1", Method: method, Params: params}
	return client.dispatch(ctx, method, payload, result, false)
}

/** ReadBatch executes one bounded native read while preserving nested partial validity. */
func (client *RPCClient) ReadBatch(ctx context.Context, query ReadBatchQuery) (ReadBatch, error) {
	if query.List.WaitingLimit <= 0 {
		query.List.WaitingLimit = 100
	}
	if query.List.StoppedLimit <= 0 {
		query.List.StoppedLimit = 100
	}
	var active, waiting, stopped []rawDownload
	var detail rawDownload
	var uris []rawURI
	observedRaw := make(map[string]*rawDownload, len(query.ObserveGIDs))
	descriptors := []callDescriptor{
		{method: "aria2.tellActive", params: []any{downloadFields()}, apply: decodeInto(&active)},
		{method: "aria2.tellWaiting", params: []any{0, query.List.WaitingLimit, downloadFields()}, apply: decodeInto(&waiting)},
		{method: "aria2.tellStopped", params: []any{query.List.StoppedOffset, query.List.StoppedLimit, downloadFields()}, apply: decodeInto(&stopped)},
	}
	detailIndex, sourceIndex := -1, -1
	if query.DetailGID != "" {
		detailIndex = len(descriptors)
		descriptors = append(descriptors, callDescriptor{method: "aria2.tellStatus", params: []any{query.DetailGID, detailFields()}, apply: decodeInto(&detail)})
		if query.ResolveDetailSource {
			sourceIndex = len(descriptors)
			descriptors = append(descriptors, callDescriptor{method: "aria2.getUris", params: []any{query.DetailGID}, apply: decodeInto(&uris)})
		}
	}
	if len(query.ObserveGIDs) > 300 {
		return ReadBatch{}, errors.New("dashboard observation capacity exceeded")
	}
	observedIndexes := make(map[int]string, len(query.ObserveGIDs))
	for _, gid := range query.ObserveGIDs {
		value := new(rawDownload)
		observedRaw[gid] = value
		index := len(descriptors)
		observedIndexes[index] = gid
		descriptors = append(descriptors, callDescriptor{method: "aria2.tellStatus", params: []any{gid, downloadFields()}, apply: decodeInto(value)})
	}
	errs := client.multicall(ctx, descriptors)
	if len(errs) == 1 && len(descriptors) != 1 && errs[0] != nil {
		return ReadBatch{}, errs[0]
	}
	read := ReadBatch{}
	read.ListErr = errors.Join(errs[0], errs[1], errs[2])
	if read.ListErr == nil {
		read.Downloads = DownloadSnapshot{Active: mapDownloads(active), Waiting: mapDownloads(waiting), Stopped: filterMetadataStopped(mapDownloads(stopped))}
	}
	if detailIndex >= 0 {
		read.DetailErr = errs[detailIndex]
		if read.DetailErr == nil {
			value := detail.toDetail()
			if value.PrimaryURI == "" && sourceIndex >= 0 && errs[sourceIndex] == nil && len(uris) > 0 {
				value.PrimaryURI = uris[0].URI
			}
			read.Detail = &value
		}
	}
	if sourceIndex >= 0 {
		read.DetailSourceErr = errs[sourceIndex]
	}
	read.Observed = make(map[string]*Download, len(query.ObserveGIDs))
	for index, gid := range observedIndexes {
		if errs[index] == nil {
			value := observedRaw[gid].toDownload()
			read.Observed[gid] = &value
			continue
		}
		if IsNotFound(errs[index]) {
			read.Observed[gid] = nil
			continue
		}
		read.ListErr = errors.Join(read.ListErr, errs[index])
	}
	return read, nil
}

func decodeInto(target any) func(json.RawMessage) error {
	return func(raw json.RawMessage) error { return json.Unmarshal(raw, target) }
}
