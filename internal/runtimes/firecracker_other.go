//go:build !linux

package runtimes

import (
	"context"
	"fmt"
)

type Firecracker struct {
	Config FirecrackerConfig
	Scope  string
}

func NewFirecracker(config FirecrackerConfig, _ *Docker, scope string) *Firecracker {
	return &Firecracker{Config: config, Scope: scope}
}
func (f *Firecracker) Type() Type { return TypeFirecracker }
func (f *Firecracker) Available(context.Context) error {
	return fmt.Errorf("%w: firecracker requires Linux", ErrUnavailable)
}
func (f *Firecracker) Create(ctx context.Context, _ Spec) (Instance, error) {
	return nil, f.Available(ctx)
}
func (f *Firecracker) Leftovers(context.Context) ([]string, error) { return nil, nil }
func (f *Firecracker) DestroyByID(context.Context, string) error   { return nil }
