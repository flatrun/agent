package api

import "encoding/json"

// The shapes an endpoint answers in. A client switches on the shape, so it lays out a collection
// of anything without being taught the resource.

type List[T any] struct {
	Items []T `json:"items"`
	// Total is what exists, which is more than Items holds once anything is paged.
	Total int `json:"total"`

	// The name this collection used to answer under, and any field it answered alongside,
	// written out until the clients reading them have moved.
	legacy string
	extras map[string]any
}

func NewList[T any](items []T, legacy string) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Items: items, Total: len(items), legacy: legacy}
}

// OfTotal says how many exist when Items is one page of them.
func (l List[T]) OfTotal(total int) List[T] {
	l.Total = total
	return l
}

// Also carries a field this collection answered with before it took this shape.
func (l List[T]) Also(name string, value any) List[T] {
	if l.extras == nil {
		l.extras = map[string]any{}
	}
	l.extras[name] = value
	return l
}

func (l List[T]) MarshalJSON() ([]byte, error) {
	out := map[string]any{"items": l.Items, "total": l.Total}
	if l.legacy != "" {
		out[l.legacy] = l.Items
	}
	for name, value := range l.extras {
		out[name] = value
	}
	return json.Marshal(out)
}

type Item[T any] struct {
	Item T `json:"item"`
}

type Message struct {
	Message string `json:"message"`
}
