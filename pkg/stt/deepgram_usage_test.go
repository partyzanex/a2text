package stt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchDeepgramBalance_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Token sk-test", r.Header.Get("Authorization"))

		switch r.URL.Path {
		case "/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"project_id":"proj-1","name":"main"}]}`))
		case "/v1/projects/proj-1/balances":
			_, _ = w.Write([]byte(`{"balances":[
				{"amount": 12.34, "units":"usd"},
				{"amount": 5.5, "units":"hour"}
			]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	got, err := FetchDeepgramBalance(context.Background(), "sk-test", srv.URL)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.InDelta(t, 12.34, got[0].Amount, 1e-6)
	assert.Equal(t, "usd", got[0].Units)
}

func TestFetchDeepgramBalance_EmptyKey(t *testing.T) {
	_, err := FetchDeepgramBalance(context.Background(), "", "")
	require.Error(t, err)
}

func TestFetchDeepgramBalance_NoProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"projects":[]}`))
	}))
	defer srv.Close()

	_, err := FetchDeepgramBalance(context.Background(), "sk-test", srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no projects")
}

func TestFetchDeepgramBalance_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"insufficient permissions"}`))
	}))
	defer srv.Close()

	_, err := FetchDeepgramBalance(context.Background(), "sk-bad", srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestFetchDeepgramBalance_InsufficientScope_ReturnsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects" {
			_, _ = w.Write([]byte(`{"projects":[{"project_id":"p1","name":"main"}]}`))

			return
		}

		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{
			"category":"INSUFFICIENT_PERMISSIONS",
			"message":"missing scope",
			"details":"Check that your account has the 'billing:read' scope"
		}`))
	}))
	defer srv.Close()

	_, err := FetchDeepgramBalance(context.Background(), "sk-scope", srv.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeepgramInsufficientScope)
}

func TestFormatDeepgramBalances(t *testing.T) {
	tests := []struct {
		name string
		in   []DeepgramBalance
		want string
	}{
		{name: "empty", in: nil, want: ""},
		{name: "usd", in: []DeepgramBalance{{Amount: 12.34, Units: "usd"}}, want: "$12.34"},
		{name: "hour", in: []DeepgramBalance{{Amount: 5.5, Units: "hour"}}, want: "5.50 h"},
		{
			name: "mixed",
			in: []DeepgramBalance{
				{Amount: 12.34, Units: "usd"},
				{Amount: 5.5, Units: "hour"},
			},
			want: "$12.34, 5.50 h",
		},
		{name: "unknown unit", in: []DeepgramBalance{{Amount: 1.0, Units: "credits"}}, want: "1.00 credits"},
		{name: "no unit", in: []DeepgramBalance{{Amount: 3.0, Units: ""}}, want: "3.00"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, FormatDeepgramBalances(tc.in))
		})
	}
}

func TestDeepgramAPIPath(t *testing.T) {
	got, err := deepgramAPIPath("https://example.com", "/v1/projects")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/v1/projects", got)
}

// Regression: legacy configs stored the full /v1/listen URL in base_url.
// Usage and models endpoints live at the API root, so anything past the
// host must be stripped — otherwise we'd hit /v1/listen/v1/projects → 404.
func TestDeepgramAPIPath_StripsLegacyListenSuffix(t *testing.T) {
	got, err := deepgramAPIPath("https://api.deepgram.com/v1/listen", "/v1/projects")
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepgram.com/v1/projects", got)
}

func TestDeepgramAPIPath_BareHost(t *testing.T) {
	got, err := deepgramAPIPath("api.deepgram.com", "/v1/models")
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepgram.com/v1/models", got)
}
