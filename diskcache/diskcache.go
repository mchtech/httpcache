// Package diskcache provides an implementation of httpcache.Cache that uses the diskv package
// to supplement an in-memory map with persistent storage
//
package diskcache

import (
	"crypto/md5"
	"encoding/hex"
	"io"

	"github.com/peterbourgon/diskv/v3"
)

// Cache is an implementation of httpcache.Cache that supplements the in-memory map with persistent storage
type Cache struct {
	d *diskv.Diskv
}

// Has return whether key has been cached
func (c *Cache) Has(key string) (ok bool) {
	return c.d.Has(keyToFilename(key))
}

// Get returns the response corresponding to key if present
func (c *Cache) Get(key string) (resp io.ReadCloser, ok bool) {
	key = keyToFilename(key)
	if stream, err := c.d.ReadStream(key, true); err == nil {
		return stream, true
	}
	return nil, false
}

// Set saves a response to the cache as key
func (c *Cache) Set(key string, resp io.ReadCloser) {
	key = keyToFilename(key)
	c.d.WriteStream(key, resp, true)
}

// Delete removes the response with key from the cache
func (c *Cache) Delete(key string) {
	key = keyToFilename(key)
	c.d.Erase(key)
}

func keyToFilename(key string) string {
	h := md5.New()
	io.WriteString(h, key)
	return hex.EncodeToString(h.Sum(nil))
}

// New returns a new Cache that will store files in basePath
func New(basePath string) *Cache {
	return &Cache{
		d: diskv.New(diskv.Options{
			BasePath:     basePath,
			CacheSizeMax: 100 * 1024 * 1024, // 100MB
		}),
	}
}

// NewWithDiskv returns a new Cache using the provided Diskv as underlying
// storage.
func NewWithDiskv(d *diskv.Diskv) *Cache {
	return &Cache{d}
}
