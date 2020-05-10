package badgercache

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"

	badger "github.com/dgraph-io/badger/v2"
)

// Cache is an implementation of httpcache.Cache with badger storage
type Cache struct {
	db *badger.DB
}

// Has returns whether key has been cached
func (c *Cache) Has(key string) (ok bool) {
	c.db.View(func(txn *badger.Txn) (err error) {
		_, err = txn.Get([]byte(key))
		ok = err == nil
		return
	})
	return
}

// Get returns the response corresponding to key if present
func (c *Cache) Get(key string) (resp io.ReadCloser, ok bool) {
	c.db.View(func(txn *badger.Txn) (err error) {
		var item *badger.Item
		item, err = txn.Get([]byte(key))
		if err != nil {
			return
		}
		var data []byte
		if data, err = item.ValueCopy(nil); err != nil {
			return
		}
		resp = ioutil.NopCloser(bytes.NewReader(data))
		ok = true
		return
	})
	return
}

// Set saves a response to the cache as key
func (c *Cache) Set(key string, resp io.ReadCloser) {
	err := c.db.Update(func(txn *badger.Txn) error {
		data, err := ioutil.ReadAll(resp)
		if err != nil {
			return err
		}
		return txn.Set([]byte(key), data)
	})
	fmt.Println(err)
}

// Delete removes the response with key from the cache
func (c *Cache) Delete(key string) {
	c.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

// New returns a new Cache that will store badger in path
func New(path string) (*Cache, error) {
	cache := &Cache{}

	var err error
	cache.db, err = badger.Open(badger.DefaultOptions(path))

	if err != nil {
		return nil, err
	}
	return cache, nil
}

// NewWithDB returns a new Cache using the provided badger as underlying
// storage.
func NewWithDB(db *badger.DB) *Cache {
	return &Cache{db}
}
