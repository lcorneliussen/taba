package wtc

import "context"

// WithStatusContext confines read cancellation to this operation. The validated
// receiver and its policy remain unchanged; compatibility callers keep their
// background context. Never mutate the original context while it is in use.
func (c *Context) WithStatusContext(ctx context.Context) *Context {
	copy := *c
	copy.readCtx = ctx
	if copy.runtimeDeclarationsCache == nil {
		copy.runtimeDeclarationsCache = newRuntimeDeclarationCache()
	}
	if c.Project != nil {
		project := *c.Project
		project.readCtx = ctx
		copy.Project = &project
	}
	return &copy
}

func (c *Context) resourceContext() context.Context {
	if c != nil && c.readCtx != nil {
		return c.readCtx
	}
	return context.Background()
}
func (p *ProjectContext) resourceContext() context.Context {
	if p != nil && p.readCtx != nil {
		return p.readCtx
	}
	return context.Background()
}

// StatusReadContext reopens the same receiver at an observation boundary. It
// shares only advisory declaration data, never stale registry or scope grants.
func (c *Context) StatusReadContext(ctx context.Context) (*Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fresh, err := OpenCollectionContext(ctx, c.Collection)
	if err != nil {
		return nil, err
	}
	fresh.StatusProgress = c.StatusProgress
	fresh.statusSweep = c.statusSweep
	fresh.runtimeDeclarationsCache = c.runtimeDeclarationsCache
	return fresh.WithStatusContext(ctx), nil
}
