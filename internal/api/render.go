package api

import "encoding/json"

// The shapes an endpoint answers in. A client switches on the shape, so it lays out a collection
// of anything without being taught the resource.

type List[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`

	// The name this collection used to answer under, written alongside items until the clients
	// reading it have moved.
	legacy string
}

func NewList[T any](items []T, legacy string) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Items: items, Total: len(items), legacy: legacy}
}

func (l List[T]) MarshalJSON() ([]byte, error) {
	out := map[string]any{"items": l.Items, "total": l.Total}
	if l.legacy != "" {
		out[l.legacy] = l.Items
	}
	return json.Marshal(out)
}

type Item[T any] struct {
	Item T `json:"item"`
}

type Message struct {
	Message string `json:"message"`
}
