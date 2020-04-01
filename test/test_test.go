package test_test

import (
	"testing"

	"github.com/mchtech/httpcache"
	"github.com/mchtech/httpcache/test"
)

func TestMemoryCache(t *testing.T) {
	test.Cache(t, httpcache.NewMemoryCache())
}
