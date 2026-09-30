package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/go-chi/chi/v5"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// validator checks requests against the embedded contract (patterns, lengths, required and unknown fields),
// which the generated binding does not. It runs after chi routed the request, so it looks the operation up by
// route pattern instead of routing a second time; the generated routes use the spec's path templates verbatim.
type validator struct {
	routes map[string]*routers.Route // "METHOD /path/{template}"
	prefix string
	onErr  func(http.ResponseWriter, *http.Request, error)
}

func newValidator(spec *openapi3.T, prefix string, onErr func(http.ResponseWriter, *http.Request, error)) *validator {
	v := &validator{routes: map[string]*routers.Route{}, prefix: prefix, onErr: onErr}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			v.routes[method+" "+path] = &routers.Route{Spec: spec, Path: path, PathItem: item, Method: method, Operation: op}
		}
	}
	return v
}

var validationOptions = &openapi3filter.Options{
	MultiError:          true,
	SkipSettingDefaults: true, // the handlers apply defaults; the body must stay as hashed for idempotency
	AuthenticationFunc:  openapi3filter.NoopAuthenticationFunc,
}

// streamedOptions validate parameters only: artifact uploads and NDJSON log batches are read by their handlers as
// streams (and checked there: the content hash, each log line), never buffered by the validator.
var streamedOptions = &openapi3filter.Options{
	MultiError:          true,
	SkipSettingDefaults: true,
	ExcludeRequestBody:  true,
	AuthenticationFunc:  openapi3filter.NoopAuthenticationFunc,
}

func streamedBody(op *openapi3.Operation) bool {
	if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil {
		return false
	}
	c := op.RequestBody.Value.Content
	return c.Get("application/octet-stream") != nil || c.Get("application/x-ndjson") != nil
}

func (v *validator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := chi.RouteContext(r.Context())
		route := v.routes[r.Method+" "+strings.TrimPrefix(rc.RoutePattern(), v.prefix)]
		if route == nil {
			next.ServeHTTP(w, r)
			return
		}
		params := make(map[string]string, len(rc.URLParams.Keys))
		for i, k := range rc.URLParams.Keys {
			val, err := url.PathUnescape(rc.URLParams.Values[i])
			if err != nil {
				val = rc.URLParams.Values[i]
			}
			params[k] = val
		}
		opts := validationOptions
		if streamedBody(route.Operation) {
			opts = streamedOptions
		}
		err := openapi3filter.ValidateRequest(r.Context(), &openapi3filter.RequestValidationInput{
			Request: r, PathParams: params, Route: route, Options: opts,
		})
		if err != nil {
			v.onErr(w, r, requestProblem(err))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestProblem turns validation errors into validation-failed with one entry per field, or bad-request when
// the body could not be parsed at all.
func requestProblem(err error) error {
	var fields []problems.FieldError
	malformed := collect(err, "", &fields)
	if malformed != "" {
		return problems.BadRequest.New("%s", malformed)
	}
	return problems.Validation(fields)
}

// collect appends field errors under the location where; it returns a message when the request is unreadable.
func collect(err error, where string, out *[]problems.FieldError) (malformed string) {
	var (
		multi  openapi3.MultiError
		reqErr *openapi3filter.RequestError
		schErr *openapi3.SchemaError
		parse  *openapi3filter.ParseError
	)
	switch {
	case errors.As(err, &multi):
		for _, e := range multi {
			if m := collect(e, where, out); m != "" {
				return m
			}
		}
	case errors.As(err, &reqErr):
		switch {
		case reqErr.Parameter != nil:
			where = reqErr.Parameter.In + "." + reqErr.Parameter.Name
		case reqErr.RequestBody != nil:
			where = "body"
		}
		if reqErr.Err == nil {
			*out = append(*out, problems.FieldError{Path: where, Message: reqErr.Reason})
			return ""
		}
		return collect(reqErr.Err, where, out)
	case errors.As(err, &parse):
		return "cannot parse " + where + ": " + parse.Reason
	case errors.As(err, &schErr):
		path := where
		if ptr := schErr.JSONPointer(); len(ptr) > 0 {
			path = "/" + strings.Join(ptr, "/")
			if where != "body" {
				path = where + path
			}
		}
		*out = append(*out, problems.FieldError{Path: path, Message: schErr.Reason})
	default:
		*out = append(*out, problems.FieldError{Path: where, Message: err.Error()})
	}
	return ""
}
