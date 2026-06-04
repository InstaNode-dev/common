// Package flagd registers the OpenFeature + flagd ("flagd") feature-flag
// backend with the parent featureflag registry.
//
// flagd is a tiny stateless Go container that streams flag definitions over
// gRPC, so a flag flip propagates sub-second to every connected service — the
// near-realtime bar the static (file/ConfigMap) backend cannot meet. This
// package wraps flagd behind the parent featureflag.Provider interface via the
// OpenFeature Go SDK, so call sites are identical regardless of backend and the
// fail-closed wrapper (featureflag.Wrap) applies uniformly.
//
// Import it for its side effect to make "flagd" resolvable through the registry:
//
//	import _ "instant.dev/common/featureflag/flagd"
//
// Keeping the OpenFeature + gRPC transitive deps in this subpackage means
// `common` consumers that only use the static backend don't pull the flagd SDK
// into their import graph (same isolation as queueprovider's broker subpackages).
//
// # Fail-closed at construction
//
// builder does NOT block on flagd readiness. If flagd is unreachable at boot,
// the OpenFeature client serves the caller default (the wrapper enforces this),
// so a flagd outage degrades every flag to OFF rather than erroring the service.
// Factory's own degrade path additionally swaps to the static backend when
// construction itself fails.
package flagd

import (
	"context"
	"fmt"
	"sync"

	flagdprovider "github.com/open-feature/go-sdk-contrib/providers/flagd/pkg"
	"github.com/open-feature/go-sdk/openfeature"

	"instant.dev/common/featureflag"
)

const (
	defaultHost = "localhost"
	// defaultRPCPort is flagd's default rpc-resolver gRPC port.
	defaultRPCPort uint16 = 8013
)

// domainSeq makes each provider instance bind to a UNIQUE OpenFeature domain so
// multiple builder calls (tests, multi-tenant boots) never clash on the SDK's
// process-global named-provider registry.
var domainSeq struct {
	sync.Mutex
	n int
}

func nextDomain() string {
	domainSeq.Lock()
	defer domainSeq.Unlock()
	domainSeq.n++
	return fmt.Sprintf("instant.featureflag.flagd.%d", domainSeq.n)
}

func init() {
	featureflag.Register(featureflag.BackendFlagd, builder)
}

// builder is the featureflag.Builder registered under "flagd". It constructs a
// real flagd OpenFeature provider and hands it to bindProvider.
func builder(cfg featureflag.Config) (featureflag.Provider, error) {
	host := cfg.FlagdHost
	if host == "" {
		host = defaultHost
	}
	port := cfg.FlagdPort
	if port == 0 {
		port = defaultRPCPort
	}

	opts := []flagdprovider.ProviderOption{
		flagdprovider.WithHost(host),
		flagdprovider.WithPort(port),
	}
	if cfg.FlagdInProcess {
		opts = append(opts, flagdprovider.WithInProcessResolver())
	} else {
		opts = append(opts, flagdprovider.WithRPCResolver())
	}

	fp := flagdprovider.NewProvider(opts...)
	return bindProvider(fp, func() { fp.Shutdown() })
}

// bindProvider binds an OpenFeature provider to a private domain and returns the
// featureflag.Provider adapter. Split out from builder as a test seam: a test
// can inject OpenFeature's in-memory provider to exercise the clean-hit
// resolution paths without a running flagd. closeFn releases the backend's
// resources (the flagd gRPC stream).
func bindProvider(fp openfeature.FeatureProvider, closeFn func()) (featureflag.Provider, error) {
	domain := nextDomain()
	// SetNamedProvider binds fp to a private domain on the OpenFeature singleton
	// without disturbing any other domain's provider. We do NOT use the
	// blocking ...AndWait variant: blocking on flagd readiness at boot would
	// turn a flagd outage into a service-boot failure, which violates fail-open
	// boot / fail-closed eval.
	if err := openfeature.SetNamedProvider(domain, fp); err != nil {
		return nil, fmt.Errorf("featureflag/flagd: bind provider: %w", err)
	}
	return &provider{
		client:  openfeature.NewClient(domain),
		closeFn: closeFn,
	}, nil
}

// provider adapts an OpenFeature client (backed by flagd) to
// featureflag.Provider. Returned values flow through featureflag.Wrap at the
// factory boundary, so any OpenFeature resolution error here collapses to the
// caller default.
type provider struct {
	client  *openfeature.Client
	closeFn func()
}

func (*provider) Name() string { return featureflag.BackendFlagd }

// Close shuts the flagd provider's gRPC stream down. Idempotent; safe on a
// never-connected provider.
func (p *provider) Close() error {
	if p.closeFn != nil {
		p.closeFn()
	}
	return nil
}

func (p *provider) BoolVariant(ctx context.Context, key string, def bool, ec featureflag.EvalContext) (bool, error) {
	v, err := p.client.BooleanValue(ctx, key, def, evalContext(ec))
	if err != nil {
		return def, err
	}
	return v, nil
}

func (p *provider) StringVariant(ctx context.Context, key, def string, ec featureflag.EvalContext) (string, error) {
	v, err := p.client.StringValue(ctx, key, def, evalContext(ec))
	if err != nil {
		return def, err
	}
	return v, nil
}

func (p *provider) IntVariant(ctx context.Context, key string, def int64, ec featureflag.EvalContext) (int64, error) {
	v, err := p.client.IntValue(ctx, key, def, evalContext(ec))
	if err != nil {
		return def, err
	}
	return v, nil
}

// evalContext renders featureflag.EvalContext into the OpenFeature evaluation
// context flagd's targeting rules consume. The exported renderer on EvalContext
// keeps the attribute key contract (team_id/tier/env) in one place.
func evalContext(ec featureflag.EvalContext) openfeature.EvaluationContext {
	return openfeature.NewEvaluationContext(ec.TargetingKey, ec.AttributeMap())
}
