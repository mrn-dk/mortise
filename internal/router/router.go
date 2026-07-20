// Package router resolves a client-visible model name to a backend pool.
package router

import (
	"fmt"

	"github.com/mrn-dk/mortise/internal/config"
)

// Router maps model names to pools.
type Router struct {
	routes map[string]*config.Pool
	models []string
}

// New builds a Router from config. It assumes cfg has been validated.
func New(cfg *config.Config) *Router {
	pools := make(map[string]*config.Pool, len(cfg.Pools))
	for i := range cfg.Pools {
		pools[cfg.Pools[i].Name] = &cfg.Pools[i]
	}
	routes := make(map[string]*config.Pool, len(cfg.Routes))
	models := make([]string, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		routes[r.Model] = pools[r.Pool]
		models = append(models, r.Model)
	}
	return &Router{routes: routes, models: models}
}

// Resolve returns the pool serving the given model, or an error if unknown.
func (r *Router) Resolve(model string) (*config.Pool, error) {
	p, ok := r.routes[model]
	if !ok {
		return nil, fmt.Errorf("no route for model %q", model)
	}
	return p, nil
}

// Models lists the public model names mortise serves.
func (r *Router) Models() []string {
	out := make([]string, len(r.models))
	copy(out, r.models)
	return out
}
