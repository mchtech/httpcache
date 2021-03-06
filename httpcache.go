// Package httpcache provides a http.RoundTripper implementation that works as a
// mostly RFC-compliant cache for http responses.
//
// It is only suitable for use as a 'private' cache (i.e. for a web-browser or an API-client
// and not for a shared proxy).
//
package httpcache

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

const (
	stale = iota
	fresh
	transparent
	// XFromCache is the header added to responses that are returned from the cache
	XFromCache = "X-Proxy-Cache"
)

// FreshnessToString map
var FreshnessToString = map[int]string{
	stale:       "stale",
	fresh:       "fresh",
	transparent: "transparent",
}

// ProxyFromCacheToString map
var ProxyFromCacheToString = map[int]string{
	0: "miss",
	1: "hit",
}

// ProxyCachedToString map
var ProxyCachedToString = map[int]string{
	0: "no-cache",
	1: "cached",
}

// ProxyWriteCacheToString map
var ProxyWriteCacheToString = map[int]string{
	0: "no-store",
	1: "store",
}

// ProxyStaleClientToString map
var ProxyStaleClientToString = map[int]string{
	-1: "use-none",
	0:  "use-cache-header",
	1:  "use-client-header",
}

// NotModifiedDelHeaders -
var NotModifiedDelHeaders = []string{
	"Content-Length",
	"Content-Type",
	"Last-Modified",
	"Status", // HTTP 2.0
}

// A Cache interface is used by the Transport to store and retrieve responses.
type Cache interface {
	// Has returns whether key has been cached
	Has(key string) (ok bool)
	// Get returns the []byte representation of a cached response and a bool
	// set to true if the value isn't empty
	Get(key string) (responseBytes io.ReadCloser, ok bool)
	// Set stores the []byte representation of a response against a key
	Set(key string, responseBytes io.ReadCloser)
	// Delete removes the value associated with the key
	Delete(key string)
}

type contextKey struct {
	name string
}

func (k *contextKey) String() string { return "httpcache context value " + k.name }

// CacheRangeContextKey -
var CacheRangeContextKey = &contextKey{"cache-range"}

// CacheKey returns the cache key for req.
func CacheKey(req *http.Request) (key string) {
	defer func() {
		if v := req.Context().Value(CacheRangeContextKey); v != nil {
			key += "-" + req.Header.Get("Range")
			return
		}
	}()
	if req.Method == http.MethodGet {
		return req.URL.String()
	} else {
		return req.Method + " " + req.URL.String()
	}
}

// CachedResponse returns the cached http.Response for req if present, and nil
// otherwise.
func CachedResponse(c Cache, req *http.Request) (resp *http.Response, err error) {
	cachedVal, ok := c.Get(CacheKey(req))
	if !ok {
		return
	}
	return http.ReadResponse(bufio.NewReader(cachedVal), req)
}

// MemoryCache is an implemtation of Cache that stores responses in an in-memory map.
type MemoryCache struct {
	mu    sync.RWMutex
	items map[string][]byte
}

// Get returns the []byte representation of the response and true if present, false if not
func (c *MemoryCache) Get(key string) (resp io.ReadCloser, ok bool) {
	var data []byte
	c.mu.RLock()
	data, ok = c.items[key]
	resp = ioutil.NopCloser(bytes.NewReader(data))
	c.mu.RUnlock()
	return resp, ok
}

// Has returns whether key has been cached
func (c *MemoryCache) Has(key string) (ok bool) {
	c.mu.RLock()
	_, ok = c.items[key]
	c.mu.RUnlock()
	return ok
}

// Set saves response resp to the cache with key
func (c *MemoryCache) Set(key string, resp io.ReadCloser) {
	c.mu.Lock()
	c.items[key], _ = ioutil.ReadAll(resp)
	c.mu.Unlock()
}

// Delete removes key from the cache
func (c *MemoryCache) Delete(key string) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

// NewMemoryCache returns a new Cache that will store items in an in-memory map
func NewMemoryCache() *MemoryCache {
	c := &MemoryCache{items: map[string][]byte{}}
	return c
}

// Transport is an implementation of http.RoundTripper that will return values from a cache
// where possible (avoiding a network request) and will additionally add validators (etag/if-modified-since)
// to repeated requests allowing servers to return 304 / Not Modified
type Transport struct {
	// The RoundTripper interface actually used to make requests
	// If nil, http.DefaultTransport is used
	Transport http.RoundTripper
	Cache     Cache
	// If true, responses returned from the cache will be given an extra header, X-From-Cache
	MarkCachedResponses bool
	CanCache            func(req *http.Request, resp *http.Response) bool
}

// NewTransport returns a new Transport with the
// provided Cache implementation and MarkCachedResponses set to true
func NewTransport(c Cache) *Transport {
	return &Transport{Cache: c, MarkCachedResponses: true}
}

// Client returns an *http.Client that caches responses.
func (t *Transport) Client() *http.Client {
	return &http.Client{Transport: t}
}

// varyMatches will return false unless all of the cached values for the headers listed in Vary
// match the new request
func varyMatches(cachedResp *http.Response, req *http.Request) bool {
	for _, header := range headerAllCommaSepValues(cachedResp.Header, "vary") {
		header = http.CanonicalHeaderKey(header)
		if header != "" && req.Header.Get(header) != cachedResp.Header.Get("X-Varied-"+header) {
			return false
		}
	}
	return true
}

// RoundTrip takes a Request and returns a Response
//
// If there is a fresh Response already in cache, then it will be returned without connecting to
// the server.
//
// If there is a stale Response, then any validators it contains will be set on the new request
// to give the server a chance to respond with NotModified. If this happens, then the cached Response
// will be returned.
func (t *Transport) RoundTrip(req *http.Request) (resp *http.Response, err error) {

	var cachedResp *http.Response

	var freshness = transparent
	var staleclient = 1
	var xfromcache = 0
	var xproxycached = 0
	var xproxywrite = 0

	defer func() {
		if t.MarkCachedResponses && resp != nil {
			if cachedResp == resp {
				xfromcache = 1
			}
			var cacheStatus = fmt.Sprintf(
				"%s, %s, %s, %s, %s",
				ProxyFromCacheToString[xfromcache],
				ProxyCachedToString[xproxycached],
				FreshnessToString[freshness],
				ProxyWriteCacheToString[xproxywrite],
				ProxyStaleClientToString[staleclient],
			)
			resp.Header.Set(XFromCache, cacheStatus)
		}
	}()

	cacheKey := CacheKey(req)
	cacheable := (req.Method == "GET" || req.Method == "HEAD") && (req.Header.Get("range") == "" || nil != req.Context().Value(CacheRangeContextKey))

	if t.CanCache != nil {
		cacheable = cacheable && t.CanCache(req, resp)
	}

	if cacheable {
		cachedResp, err = CachedResponse(t.Cache, req)
	} else {
		// Need to invalidate an existing value
		t.Cache.Delete(cacheKey)
	}

	transport := t.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	if cacheable && cachedResp != nil && err == nil {
		xproxycached = 1
		if varyMatches(cachedResp, req) {
			// Can only use cached value if the new request doesn't Vary significantly
			freshness = getFreshness(cachedResp.Header, req.Header)
			if freshness == fresh {
				staleclient = -1
				cachedResp.StatusCode = http.StatusNotModified
				cachedResp.Status = http.StatusText(http.StatusNotModified)
				cachedResp.Body.Close()
				cachedResp.Body = ioutil.NopCloser(bytes.NewReader(nil))
				for _, h := range NotModifiedDelHeaders {
					cachedResp.Header.Del(h)
				}
				cachedResp.ContentLength = 0
				return cachedResp, nil
			}

			if freshness == stale {
				var req2 *http.Request
				// Add validators if caller hasn't already done so
				etag := cachedResp.Header.Get("etag")
				// if etag != "" && req.Header.Get("if-none-match") == "" {
				if etag != "" {
					req2 = cloneRequest(req)
					req2.Header.Set("if-none-match", etag)
					staleclient = 0
				}
				lastModified := cachedResp.Header.Get("last-modified")
				// if lastModified != "" && req.Header.Get("if-modified-since") == "" {
				if lastModified != "" {
					if req2 == nil {
						req2 = cloneRequest(req)
					}
					req2.Header.Set("if-modified-since", lastModified)
					staleclient = 0
				}
				if req2 != nil {
					req = req2
				}
			}
		}

		resp, err = transport.RoundTrip(req)
		if err == nil && (req.Method == "GET" || req.Method == "HEAD") && resp.StatusCode == http.StatusNotModified {
			// Replace the 304 response with the one from cache, but update with some new headers
			endToEndHeaders := getEndToEndHeaders(resp.Header)
			for _, header := range endToEndHeaders {
				if staleclient == 0 && header == "Content-Length" {
					continue
				}
				cachedResp.Header[header] = resp.Header[header]
			}
			resp.Body.Close()
			if staleclient == 1 {
				cachedResp.StatusCode = http.StatusNotModified
				cachedResp.Status = http.StatusText(http.StatusNotModified)
			}
			resp = cachedResp
		} else if (err != nil || (cachedResp != nil && resp.StatusCode >= 500)) &&
			(req.Method == "GET" || req.Method == "HEAD") && canStaleOnError(cachedResp.Header, req.Header) {
			// In case of transport failure and stale-if-error activated, returns cached content
			// when available
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			return cachedResp, nil
		} else {
			if err != nil || resp.StatusCode != http.StatusOK {
				xproxycached = 0
				t.Cache.Delete(cacheKey)
			}
			if err != nil {
				return nil, err
			}
		}
	} else {
		xproxycached = 0
		reqCacheControl := parseCacheControl(req.Header)
		if _, ok := reqCacheControl["only-if-cached"]; ok {
			resp = newGatewayTimeoutResponse(req)
		} else {
			resp, err = transport.RoundTrip(req)
			if err != nil {
				return nil, err
			}
		}
		if t.CanCache != nil {
			cacheable = cacheable && t.CanCache(req, resp)
		}
	}

	// 这里没有遵守RPC, 304 Vary头变化应更新缓存, 但实际上没更新
	if staleclient == 1 && resp.StatusCode == http.StatusNotModified {
		resp.Body.Close()
		resp.Body = ioutil.NopCloser(bytes.NewReader(nil))
		for _, h := range NotModifiedDelHeaders {
			resp.Header.Del(h)
		}
		resp.ContentLength = 0
	}

	if cacheable && canStore(parseCacheControl(req.Header), parseCacheControl(resp.Header), resp) {
		for _, varyKey := range headerAllCommaSepValues(resp.Header, "vary") {
			varyKey = http.CanonicalHeaderKey(varyKey)
			fakeHeader := "X-Varied-" + varyKey
			reqValue := req.Header.Get(varyKey)
			if reqValue != "" {
				resp.Header.Set(fakeHeader, reqValue)
			}
		}
		if staleclient == 1 && resp.StatusCode == http.StatusNotModified {
			return resp, nil
		}
		if resp != cachedResp {
			xproxywrite = 1
			switch req.Method {
			case "GET":
				// Delay caching until EOF is reached.
				resp.Body = &cachingReadCloser{
					R: resp.Body,
					OnEOF: func(r io.Reader) {
						resp := *resp
						resp.Body = ioutil.NopCloser(r)
						respBytes, err := httputil.DumpResponse(&resp, true)
						if err == nil {
							t.Cache.Set(cacheKey, ioutil.NopCloser(bytes.NewReader(respBytes)))
						}
					},
				}
			default:
				respBytes, err := httputil.DumpResponse(resp, true)
				if err == nil {
					t.Cache.Set(cacheKey, ioutil.NopCloser(bytes.NewReader(respBytes)))
				}
			}
		}
	} else {
		xproxycached = 0
		t.Cache.Delete(cacheKey)
	}
	return resp, nil
}

// ErrNoDateHeader indicates that the HTTP headers contained no Date header.
var ErrNoDateHeader = errors.New("no Date header")

// Date parses and returns the value of the Date header.
func Date(respHeaders http.Header) (date time.Time, err error) {
	dateHeader := respHeaders.Get("date")
	if dateHeader == "" {
		err = ErrNoDateHeader
		return
	}

	return time.Parse(time.RFC1123, dateHeader)
}

type realClock struct{}

func (c *realClock) since(d time.Time) time.Duration {
	return time.Since(d)
}

type timer interface {
	since(d time.Time) time.Duration
}

var clock timer = &realClock{}

// getFreshness will return one of fresh/stale/transparent based on the cache-control
// values of the request and the response
//
// fresh indicates the response can be returned
// stale indicates that the response needs validating before it is returned
// transparent indicates the response should not be used to fulfil the request
//
// Because this is only a private cache, 'public' and 'private' in cache-control aren't
// signficant. Similarly, smax-age isn't used.
func getFreshness(respHeaders, reqHeaders http.Header) (freshness int) {
	respCacheControl := parseCacheControl(respHeaders)
	reqCacheControl := parseCacheControl(reqHeaders)
	if _, ok := reqCacheControl["no-cache"]; ok {
		return transparent
	}
	if _, ok := respCacheControl["no-cache"]; ok {
		return stale
	}
	if _, ok := reqCacheControl["only-if-cached"]; ok {
		return fresh
	}

	date, err := Date(respHeaders)
	if err != nil {
		return stale
	}
	currentAge := clock.since(date)

	var lifetime time.Duration
	var zeroDuration time.Duration

	// If a response includes both an Expires header and a max-age directive,
	// the max-age directive overrides the Expires header, even if the Expires header is more restrictive.
	if maxAge, ok := respCacheControl["max-age"]; ok {
		lifetime, err = time.ParseDuration(maxAge + "s")
		if err != nil {
			lifetime = zeroDuration
		}
	} else {
		expiresHeader := respHeaders.Get("Expires")
		if expiresHeader != "" {
			expires, err := time.Parse(time.RFC1123, expiresHeader)
			if err != nil {
				lifetime = zeroDuration
			} else {
				lifetime = expires.Sub(date)
			}
		}
	}

	if maxAge, ok := reqCacheControl["max-age"]; ok {
		// the client is willing to accept a response whose age is no greater than the specified time in seconds
		lifetime, err = time.ParseDuration(maxAge + "s")
		if err != nil {
			lifetime = zeroDuration
		}
	}
	if minfresh, ok := reqCacheControl["min-fresh"]; ok {
		//  the client wants a response that will still be fresh for at least the specified number of seconds.
		minfreshDuration, err := time.ParseDuration(minfresh + "s")
		if err == nil {
			currentAge = time.Duration(currentAge + minfreshDuration)
		}
	}

	if maxstale, ok := reqCacheControl["max-stale"]; ok {
		// Indicates that the client is willing to accept a response that has exceeded its expiration time.
		// If max-stale is assigned a value, then the client is willing to accept a response that has exceeded
		// its expiration time by no more than the specified number of seconds.
		// If no value is assigned to max-stale, then the client is willing to accept a stale response of any age.
		//
		// Responses served only because of a max-stale value are supposed to have a Warning header added to them,
		// but that seems like a  hassle, and is it actually useful? If so, then there needs to be a different
		// return-value available here.
		if maxstale == "" {
			return fresh
		}
		maxstaleDuration, err := time.ParseDuration(maxstale + "s")
		if err == nil {
			currentAge = time.Duration(currentAge - maxstaleDuration)
		}
	}

	if lifetime > currentAge {
		var fe, fl bool
		inm := reqHeaders.Get("if-none-match")
		etag := respHeaders.Get("etag")
		if inm == etag && inm != "" {
			fe = true
		}
		ims := reqHeaders.Get("if-modified-since")
		lm := respHeaders.Get("last-modified")
		if ims == lm && ims != "" {
			fl = true
		}
		// 两个都一样 或 一个为空一个一样
		if (fe && fl) || (fe && (ims == "" || lm == "")) || (fl && (inm == "" || etag == "")) {
			return fresh
		}
	}

	return stale
}

// Returns true if either the request or the response includes the stale-if-error
// cache control extension: https://tools.ietf.org/html/rfc5861
func canStaleOnError(respHeaders, reqHeaders http.Header) bool {
	respCacheControl := parseCacheControl(respHeaders)
	reqCacheControl := parseCacheControl(reqHeaders)

	var err error
	lifetime := time.Duration(-1)

	if staleMaxAge, ok := respCacheControl["stale-if-error"]; ok {
		if staleMaxAge != "" {
			lifetime, err = time.ParseDuration(staleMaxAge + "s")
			if err != nil {
				return false
			}
		} else {
			return true
		}
	}
	if staleMaxAge, ok := reqCacheControl["stale-if-error"]; ok {
		if staleMaxAge != "" {
			lifetime, err = time.ParseDuration(staleMaxAge + "s")
			if err != nil {
				return false
			}
		} else {
			return true
		}
	}

	if lifetime >= 0 {
		date, err := Date(respHeaders)
		if err != nil {
			return false
		}
		currentAge := clock.since(date)
		if lifetime > currentAge {
			return true
		}
	}

	return false
}

func getEndToEndHeaders(respHeaders http.Header) []string {
	// These headers are always hop-by-hop
	hopByHopHeaders := map[string]struct{}{
		"Connection":          {},
		"Keep-Alive":          {},
		"Proxy-Authenticate":  {},
		"Proxy-Authorization": {},
		"Te":                  {},
		"Trailer":             {},
		"Transfer-Encoding":   {},
		"Upgrade":             {},
	}

	for _, extra := range strings.Split(respHeaders.Get("connection"), ",") {
		// any header listed in connection, if present, is also considered hop-by-hop
		if strings.Trim(extra, " ") != "" {
			hopByHopHeaders[http.CanonicalHeaderKey(extra)] = struct{}{}
		}
	}
	endToEndHeaders := []string{}
	for respHeader := range respHeaders {
		if _, ok := hopByHopHeaders[respHeader]; !ok {
			endToEndHeaders = append(endToEndHeaders, respHeader)
		}
	}
	return endToEndHeaders
}

func canStore(reqCacheControl, respCacheControl cacheControl, resp *http.Response) (canStore bool) {
	if _, ok := respCacheControl["no-store"]; ok {
		return false
	}
	if _, ok := reqCacheControl["no-store"]; ok {
		return false
	}
	if resp.Header.Get("etag") == "" && resp.Header.Get("last-modified") == "" {
		return false
	}
	return true
}

func newGatewayTimeoutResponse(req *http.Request) *http.Response {
	var braw bytes.Buffer
	braw.WriteString("HTTP/1.1 504 Gateway Timeout\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(&braw), req)
	if err != nil {
		panic(err)
	}
	return resp
}

// cloneRequest returns a clone of the provided *http.Request.
// The clone is a shallow copy of the struct and its Header map.
// (This function copyright goauth2 authors: https://code.google.com/p/goauth2)
func cloneRequest(r *http.Request) *http.Request {
	// shallow copy of the struct
	r2 := new(http.Request)
	*r2 = *r
	// deep copy of the Header
	r2.Header = make(http.Header)
	for k, s := range r.Header {
		r2.Header[k] = s
	}
	return r2
}

type cacheControl map[string]string

func parseCacheControl(headers http.Header) cacheControl {
	cc := cacheControl{}
	ccHeader := headers.Get("Cache-Control")
	for _, part := range strings.Split(ccHeader, ",") {
		part = strings.Trim(part, " ")
		if part == "" {
			continue
		}
		if strings.ContainsRune(part, '=') {
			keyval := strings.Split(part, "=")
			cc[strings.Trim(keyval[0], " ")] = strings.Trim(keyval[1], ",")
		} else {
			cc[part] = ""
		}
	}
	return cc
}

// headerAllCommaSepValues returns all comma-separated values (each
// with whitespace trimmed) for header name in headers. According to
// Section 4.2 of the HTTP/1.1 spec
// (http://www.w3.org/Protocols/rfc2616/rfc2616-sec4.html#sec4.2),
// values from multiple occurrences of a header should be concatenated, if
// the header's value is a comma-separated list.
func headerAllCommaSepValues(headers http.Header, name string) []string {
	var vals []string
	for _, val := range headers[http.CanonicalHeaderKey(name)] {
		fields := strings.Split(val, ",")
		for i, f := range fields {
			fields[i] = strings.TrimSpace(f)
		}
		vals = append(vals, fields...)
	}
	return vals
}

// cachingReadCloser is a wrapper around ReadCloser R that calls OnEOF
// handler with a full copy of the content read from R when EOF is
// reached.
type cachingReadCloser struct {
	// Underlying ReadCloser.
	R io.ReadCloser
	// OnEOF is called with a copy of the content of R when EOF is reached.
	OnEOF func(io.Reader)

	buf bytes.Buffer // buf stores a copy of the content of R.
}

// Read reads the next len(p) bytes from R or until R is drained. The
// return value n is the number of bytes read. If R has no data to
// return, err is io.EOF and OnEOF is called with a full copy of what
// has been read so far.
func (r *cachingReadCloser) Read(p []byte) (n int, err error) {
	n, err = r.R.Read(p)
	r.buf.Write(p[:n])
	if err == io.EOF {
		r.OnEOF(bytes.NewReader(r.buf.Bytes()))
	}
	return n, err
}

func (r *cachingReadCloser) Close() error {
	return r.R.Close()
}

// NewMemoryCacheTransport returns a new Transport using the in-memory cache implementation
func NewMemoryCacheTransport() *Transport {
	c := NewMemoryCache()
	t := NewTransport(c)
	return t
}
