package stt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepgramModelsEndpoint_DefaultBase(t *testing.T) {
	got, err := deepgramModelsEndpoint("")
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepgram.com/v1/models", got)
}

func TestDeepgramModelsEndpoint_TrailingSlash(t *testing.T) {
	got, err := deepgramModelsEndpoint("https://example.com/")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/v1/models", got)
}

func TestDeepgramModelsEndpoint_AddsHTTPSWhenSchemeMissing(t *testing.T) {
	got, err := deepgramModelsEndpoint("api.deepgram.com")
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepgram.com/v1/models", got)
}

func TestFetchDeepgramModels_EmptyKey_ReturnsError(t *testing.T) {
	_, err := FetchDeepgramModels(context.Background(), "  ", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api key")
}

func TestFetchDeepgramModels_HappyPath_ReturnsSortedUnique(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, "Token sk-test", r.Header.Get("Authorization"))

		_, _ = w.Write([]byte(`{
			"stt": [
				{"name": "Nova 2", "canonical_name": "nova-2"},
				{"name": "Nova 2 Meeting", "canonical_name": "nova-2-meeting"},
				{"name": "Dup", "canonical_name": "nova-2"},
				{"name": "Enhanced", "canonical_name": ""}
			]
		}`))
	}))
	defer srv.Close()

	got, err := FetchDeepgramModels(context.Background(), "sk-test", srv.URL)
	require.NoError(t, err)
	assert.Equal(t, []string{"Enhanced", "nova-2", "nova-2-meeting"}, got)
}

func TestFetchDeepgramModels_Non2xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer srv.Close()

	_, err := FetchDeepgramModels(context.Background(), "sk-bad", srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}
