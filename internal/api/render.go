package api

import "encoding/json"

// The shapes an endpoint answers in. They say how an answer is presented, not what it is about:
// a collection is a collection whether it holds deployments or certificates, so a client can lay
// any of them out without being taught the resource first. The generated description carries the
// shape, which is what a client switches on.

// List is every collection. Items is the whole answer; a client showing a table takes its rows
// from there and its columns from the item's own type.
type List[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`

	// legacy is the name this collection used to answer under, written alongside items until
	// the clients reading it have moved. It carries no type of its own and is not described.
	legacy string
}

// NewList answers with a collection. The legacy name may be empty for anything new, which is
// where every collection ends up.
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

// Item is one thing, presented as its fields rather than as a row.
type Item[T any] struct {
	Item T `json:"item"`
}

// Message is an answer that only reports what happened.
type Message struct {
	Message string `json:"message"`
}
