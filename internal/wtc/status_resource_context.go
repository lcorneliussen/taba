package wtc

import "context"

// WithStatusContext confines read cancellation to this operation. The validated
// receiver and its policy remain unchanged; compatibility callers keep their
// background context. Never mutate the original context while it is in use.
func (c *Context) WithStatusContext(ctx context.Context) *Context {
	copy := *c
	if copy.statusIdentityCache == nil {
		copy.statusIdentityCache = newStatusIdentityMemo()
	}
	ctx = context.WithValue(ctx, statusIdentityMemoKey{}, copy.statusIdentityCache)
	ctx = context.WithValue(ctx, statusGitMemoKey{}, copy.statusIdentityCache)
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
// carries the progress callback, sweep trust flag and advisory declaration
// cache. Registry and receiving scope are freshly re-read by OpenCollectionContext.
func (c *Context) StatusReadContext(ctx context.Context) (*Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.statusIdentityCache != nil {
		ctx = context.WithValue(ctx, statusIdentityMemoKey{}, c.statusIdentityCache)
	}
	ctx = statusResourceOperation(ctx)
	fresh, err := OpenCollectionContext(ctx, c.Collection)
	if err != nil {
		return nil, err
	}
	fresh.statusIdentityCache = c.statusIdentityCache
	fresh.StatusProgress = c.StatusProgress
	fresh.statusSweep = c.statusSweep
	fresh.runtimeDeclarationsCache = c.runtimeDeclarationsCache
	return fresh.WithStatusContext(ctx), nil
}
