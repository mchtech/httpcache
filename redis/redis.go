// Package redis provides a redis interface for http caching.
package redis

import (
	"bytes"
	"io"
	"io/ioutil"

	"github.com/gomodule/redigo/redis"
	"github.com/mchtech/httpcache"
)

// cache is an implementation of httpcache.Cache that caches responses in a
// redis server.
type cache struct {
	redis.Conn
}

// cacheKey modifies an httpcache key for use in redis. Specifically, it
// prefixes keys to avoid collision with other data stored in redis.
func cacheKey(key string) string {
	return "rediscache:" + key
}

// Has returns whether key has been cached
func (c cache) Has(key string) (ok bool) {
	ok, _ = redis.Bool(c.Do("EXISTS", cacheKey(key)))
	return
}

// Get returns the response corresponding to key if present.
func (c cache) Get(key string) (resp io.ReadCloser, ok bool) {
	item, err := redis.Bytes(c.Do("GET", cacheKey(key)))
	if err != nil {
		return nil, false
	}
	resp = ioutil.NopCloser(bytes.NewReader(item))
	return resp, true
}

// Set saves a response to the cache as key.
func (c cache) Set(key string, resp io.ReadCloser) {
	data, err := ioutil.ReadAll(resp)
	if err != nil {
		return
	}
	c.Do("SET", cacheKey(key), data)
}

// Delete removes the response with key from the cache.
func (c cache) Delete(key string) {
	c.Do("DEL", cacheKey(key))
}

// NewWithClient returns a new Cache with the given redis connection.
func NewWithClient(client redis.Conn) httpcache.Cache {
	return cache{client}
}
