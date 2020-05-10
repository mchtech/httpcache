package test

import (
	"bytes"
	"io/ioutil"
	"testing"

	"github.com/mchtech/httpcache"
)

// Cache excercises a httpcache.Cache implementation.
func Cache(t *testing.T, cache httpcache.Cache) {
	key := "testKey"

	ok := cache.Has(key)
	if ok {
		t.Fatal("retrieved key before adding it")
	}

	_, ok = cache.Get(key)
	if ok {
		t.Fatal("retrieved key before adding it")
	}

	val := []byte("some bytes")
	valStream := ioutil.NopCloser(bytes.NewReader(val))
	cache.Set(key, valStream)

	ok = cache.Has(key)
	if !ok {
		t.Fatal("could not retrieve an element we just added")
	}

	retValStream, ok := cache.Get(key)
	if !ok {
		t.Fatal("could not retrieve an element we just added")
	}
	retVal, err := ioutil.ReadAll(retValStream)
	if err != nil {
		t.Fatal("read error", err)
	}
	if !bytes.Equal(retVal, val) {
		t.Fatal("retrieved a different value than what we put in")
	}

	cache.Delete(key)

	_, ok = cache.Get(key)
	if ok {
		t.Fatal("deleted key still present")
	}
}
