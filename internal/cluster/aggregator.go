package cluster

import (
	"context"
	"encoding/json"
	"fmt"
)

type ServerResult struct {
	Name   string          `json:"name"`
	Online bool            `json:"online"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type AggregatedResponse struct {
	Servers map[string]ServerResult `json:"servers"`
}

func AggregateFromPeers(ctx context.Context, localData []byte, mgr *Manager, path string) *AggregatedResponse {
	resp := &AggregatedResponse{
		Servers: make(map[string]ServerResult),
	}

	resp.Servers[mgr.ServerName()] = ServerResult{
		Name:   mgr.ServerName(),
		Online: true,
		Data:   json.RawMessage(localData),
	}

	results := mgr.ForEachPeer(ctx, func(ctx context.Context, name string, client *Client) ([]byte, error) {
		data, status, err := client.Get(ctx, path)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("peer returned status %d", status)
		}
		return data, nil
	})

	for name, result := range results {
		if result.Error != "" {
			resp.Servers[name] = ServerResult{
				Name:   name,
				Online: false,
				Error:  result.Error,
			}
		} else {
			resp.Servers[name] = ServerResult{
				Name:   name,
				Online: true,
				Data:   json.RawMessage(result.Data),
			}
		}
	}

	return resp
}
