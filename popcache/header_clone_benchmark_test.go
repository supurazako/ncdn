package main

import (
	"net/http"
	"testing"
)

var headerCloneSink http.Header

func BenchmarkResponseHeaderClone(b *testing.B) {
	header := http.Header{
		"Content-Type":  {"application/json"},
		"Cache-Control": {"max-age=60"},
		"Etag":          {`"v1"`},
		"Vary":          {"Accept-Encoding"},
	}
	b.Run("deep", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			headerCloneSink = header.Clone()
		}
	})
	b.Run("map-only", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			headerCloneSink = cloneHeaderMapOnly(header)
		}
	})
}

// cloneHeaderMapOnly is an upper-bound experiment. It shares value slices and
// must not be used for a response whose caller can mutate header values.
func cloneHeaderMapOnly(header http.Header) http.Header {
	clone := make(http.Header, len(header)+2)
	for name, values := range header {
		clone[name] = values
	}
	return clone
}
