// Package host runs the roster: the lead, the workers and the reviewer.
package host

import (
	"context"
	"errors"

	"github.com/three-agent-problem/halo/internal/config"
)

// Provision creates the roster's addresses and saves their tokens.
func Provision(ctx context.Context, cfg *config.Config, st *config.State) error {
	return errors.New("not built yet")
}

// Run runs the roster until ctx ends.
func Run(ctx context.Context, cfg *config.Config, st *config.State) error {
	return errors.New("not built yet")
}
