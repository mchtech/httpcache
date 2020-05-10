// Package leveldbcache provides an implementation of httpcache.Cache that
// uses github.com/syndtr/goleveldb/leveldb
package leveldbcache

import (
	"bytes"
	"io"
	"io/ioutil"

	"github.com/syndtr/goleveldb/leveldb"
)

// Cache is an implementation of httpcache.Cache with leveldb storage
type Cache struct {
	db *leveldb.DB
}

// Has returns whether key has been cached
func (c *Cache) Has(key string) (ok bool) {
	ok, _ = c.db.Has([]byte(key), nil)
	return
}

// Get returns the response corresponding to key if present
func (c *Cache) Get(key string) (resp io.ReadCloser, ok bool) {
	data, err := c.db.Get([]byte(key), nil)
	if err != nil {
		return
	}
	resp = ioutil.NopCloser(bytes.NewReader(data))
	return resp, true
}

// Set saves a response to the cache as key
func (c *Cache) Set(key string, resp io.ReadCloser) {
	data, err := ioutil.ReadAll(resp)
	if err != nil {
		return
	}
	c.db.Put([]byte(key), data, nil)
}

// Delete removes the response with key from the cache
func (c *Cache) Delete(key string) {
	c.db.Delete([]byte(key), nil)
}

// New returns a new Cache that will store leveldb in path
func New(path string) (*Cache, error) {
	cache := &Cache{}

	var err error
	cache.db, err = leveldb.OpenFile(path, nil)

	if err != nil {
		return nil, err
	}
	return cache, nil
}

// NewWithDB returns a new Cache using the provided leveldb as underlying
// storage.
func NewWithDB(db *leveldb.DB) *Cache {
	return &Cache{db}
}
