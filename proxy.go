// HTTP reverse proxy to the buckets sidecar.
//
// Flow per request:
//
//  1. authAndClassify: validate the bearer against controlplane → Identity{UserID},
//     then label the operation. Both are stashed on r.Context().
//  2. ReverseProxy.Director: strip the inbound bearer (sidecar doesn't need it),
//     stamp X-Tinfoil-Tenant-Id from the validated identity (never trust the
//     client's value), pass X-Tinfoil-Encryption-Key through unchanged.
//  3. ReverseProxy.ModifyResponse: on 2xx, emit one usage event.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	usagereporting "github.com/tinfoilsh/usage-reporting-go"
)

type ctxKey int

const (
	ctxKeyIdentity ctxKey = iota
	ctxKeyOp
)

func withIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKeyIdentity, id)
}

func identityFromCtx(ctx context.Context) (Identity, bool) {
	v, ok := ctx.Value(ctxKeyIdentity).(Identity)
	return v, ok
}

func withOp(ctx context.Context, op opMeta) context.Context {
	return context.WithValue(ctx, ctxKeyOp, op)
}

func opFromCtx(ctx context.Context) (opMeta, bool) {
	v, ok := ctx.Value(ctxKeyOp).(opMeta)
	return v, ok
}

// tenantIDFor derives the sidecar tenant id from the resolved identity.
// We support users only; the namespace fits the sidecar's
// [A-Za-z0-9_-]{1,64} constraint.
func tenantIDFor(id Identity) string {
	return "user-" + id.UserID
}

// sidecarURL is the in-pod address of the buckets sidecar. Both containers
// run in the enclave's shared network namespace, so this is always loopback.
const sidecarURL = "http://localhost:9000"

// NewProxy returns the assembled middleware → ReverseProxy handler. The
// internal ReverseProxy hooks read Identity and opMeta from the request
// context — that contract is enforced by authAndClassify, which is the
// only caller that should ever wrap the inner proxy.
func NewProxy(resolver Resolver, reporter *Reporter) (http.Handler, error) {
	target, err := url.Parse(sidecarURL)
	if err != nil {
		return nil, err
	}

	rp := &httputil.ReverseProxy{}
	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)

		id, _ := identityFromCtx(pr.In.Context())
		pr.Out.Header.Set("X-Tinfoil-Tenant-Id", tenantIDFor(id))

		// Sidecar doesn't read the bearer; don't leak the api key downstream.
		pr.Out.Header.Del("Authorization")
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode/100 != 2 {
			return nil
		}
		op, ok := opFromCtx(resp.Request.Context())
		if !ok {
			return nil
		}
		id, _ := identityFromCtx(resp.Request.Context())

		var meters []usagereporting.Meter
		if op.BytesAdded > 0 {
			meters = BytesAdded(op.BytesAdded)
		}
		// TODO(bytes_removed): when the sidecar surfaces deleted-object size on
		// DELETE responses (e.g. X-Tinfoil-Bytes-Removed), wire it in here:
		//   if n := resp.Header.Get("X-Tinfoil-Bytes-Removed"); n != "" { ... BytesRemoved(...) }

		reporter.ReportOperation(resp.Request, id, op.Name, op.Class, meters, nil)
		return nil
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
	}

	return authAndClassify(resolver, rp), nil
}

// authAndClassify validates the bearer, resolves the owning user, labels the
// operation, and threads both onto the request context for the proxy hooks.
// A failed bearer aborts the request before any byte reaches the sidecar.
func authAndClassify(resolver Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey, err := bearerToken(r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}
		id, err := resolver.Resolve(r.Context(), apiKey)
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidToken):
				http.Error(w, "invalid api key", http.StatusUnauthorized)
			case errors.Is(err, ErrUpstreamUnavailable):
				http.Error(w, "identity service unavailable", http.StatusBadGateway)
			default:
				http.Error(w, "identity resolve failed", http.StatusInternalServerError)
			}
			return
		}

		// short-circuit before billing
		if r.Header.Get("X-Tinfoil-Encryption-Key") == "" {
			http.Error(w, "missing X-Tinfoil-Encryption-Key header", http.StatusBadRequest)
			return
		}

		ctx := withIdentity(r.Context(), id)
		ctx = withOp(ctx, classify(r))

		// Tripwire: empty Identity → tenant id "user-", shared anon namespace.
		if _, ok := identityFromCtx(ctx); !ok {
			http.Error(w, "internal: identity not populated", http.StatusInternalServerError)
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(h string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("missing or invalid Authorization header")
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", errors.New("empty bearer token")
	}
	return tok, nil
}
