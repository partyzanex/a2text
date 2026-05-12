package stt

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/pkg/sttx"
)

type DeepgramSuite struct {
	suite.Suite

	wavPath string
}

func TestDeepgramSuite(t *testing.T) {
	suite.Run(t, new(DeepgramSuite))
}

func (s *DeepgramSuite) SetupSuite() {
	tmpDir := s.T().TempDir()
	s.wavPath = filepath.Join(tmpDir, "test.wav")
	s.Require().NoError(os.WriteFile(s.wavPath, minimalWAVBytes(), 0o600))
}

func newTestDeepgram(baseURL string) *DeepgramTranscriber {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	return NewDeepgramTranscriber("test-api-key", baseURL, 20<<20, log)
}

// deepgramResponse builds a valid Deepgram JSON response with the given transcript.
func deepgramResponse(transcript string) map[string]any {
	return map[string]any{
		"results": map[string]any{
			"channels": []map[string]any{
				{
					"alternatives": []map[string]any{
						{"transcript": transcript},
					},
				},
			},
		},
	}
}

// --- Constructor ---

func (s *DeepgramSuite) TestNew_DefaultURL() {
	tr := NewDeepgramTranscriber("key", "", 0, nil)
	s.Equal("https://api.deepgram.com/v1/listen", tr.apiURL)
}

func (s *DeepgramSuite) TestNew_CustomURL() {
	tr := NewDeepgramTranscriber("key", "http://custom:8080/v1/listen", 0, nil)
	s.Equal("http://custom:8080/v1/listen", tr.apiURL)
}

func (s *DeepgramSuite) TestNew_ZeroMaxFileSize_UsesDefault() {
	tr := NewDeepgramTranscriber("key", "", 0, nil)
	s.Equal(int64(defaultMaxFileSize), tr.maxFileSize)
}

func (s *DeepgramSuite) TestNew_CustomMaxFileSize() {
	tr := NewDeepgramTranscriber("key", "", 5<<20, nil)
	s.Equal(int64(5<<20), tr.maxFileSize)
}

func (s *DeepgramSuite) TestName() {
	tr := newTestDeepgram("http://irrelevant")
	s.Equal("deepgram", tr.Name())
}

// --- No-ops ---

func (s *DeepgramSuite) TestLoadModel_IsNoop() {
	tr := newTestDeepgram("http://irrelevant")
	s.NoError(tr.LoadModel("/some/model.bin"))
}

func (s *DeepgramSuite) TestReloadModel_IsNoop() {
	tr := newTestDeepgram("http://irrelevant")
	s.NoError(tr.ReloadModel("new-model"))
}

func (s *DeepgramSuite) TestClose_IsNoop() {
	tr := newTestDeepgram("http://irrelevant")
	s.NoError(tr.Close())
}

func (s *DeepgramSuite) TestDetectLanguage_ReturnsAuto() {
	tr := newTestDeepgram("http://irrelevant")
	lang, err := tr.DetectLanguage(context.Background(), s.wavPath)
	s.Require().NoError(err)
	s.Equal(langAuto, lang)
}

// --- Happy path ---

func (s *DeepgramSuite) TestTranscribe_Happy_ReturnsText() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal(http.MethodPost, r.Method)
		s.Contains(r.Header.Get("Authorization"), "Token test-api-key")
		s.Equal("audio/wav", r.Header.Get("Content-Type"))
		s.Contains(r.URL.RawQuery, "model=nova-2")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(deepgramResponse("hello world"))
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	result, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().NoError(err)
	s.Equal("hello world", result)
}

func (s *DeepgramSuite) TestTranscribe_WithLanguage_SetsQueryParam() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal("ru", r.URL.Query().Get("language"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(deepgramResponse("привет"))
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	result, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().NoError(err)
	s.Equal("привет", result)
}

func (s *DeepgramSuite) TestTranscribe_AutoLang_NoLanguageParam() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Empty(r.URL.Query().Get("language"), "language param must be absent for 'auto'")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(deepgramResponse("auto result"))
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "auto")
	s.NoError(err)
}

func (s *DeepgramSuite) TestTranscribe_EmptyLang_NoLanguageParam() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Empty(r.URL.Query().Get("language"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(deepgramResponse("result"))
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "")
	s.NoError(err)
}

// --- Error cases ---

func (s *DeepgramSuite) TestTranscribe_ServerError_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "500")
}

func (s *DeepgramSuite) TestTranscribe_InvalidJSON_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json{{{"))
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
}

func (s *DeepgramSuite) TestTranscribe_EmptyChannels_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": map[string]any{
				"channels": []any{},
			},
		})
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "no transcription results")
}

func (s *DeepgramSuite) TestTranscribe_EmptyAlternatives_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": map[string]any{
				"channels": []map[string]any{
					{"alternatives": []any{}},
				},
			},
		})
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

func (s *DeepgramSuite) TestTranscribe_FileNotFound_ReturnsTranscribeFailed() {
	tr := newTestDeepgram("http://irrelevant")
	_, err := tr.Transcribe(context.Background(), "/nonexistent/file.wav", "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

func (s *DeepgramSuite) TestTranscribe_FileTooLarge_ReturnsError() {
	// Use a tiny maxFileSize so we don't need to write megabytes.
	tr := NewDeepgramTranscriber("key", "http://irrelevant", 10, nil)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru") // wavPath > 10 bytes
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "exceeds limit")
}

func (s *DeepgramSuite) TestTranscribe_OversizedResponseBody_IsHandled() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(make([]byte, 2<<20)) // 2 MB
	}))
	defer srv.Close()

	tr := newTestDeepgram(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "500")
}

func (s *DeepgramSuite) TestTranscribe_ContextCanceled_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tr := newTestDeepgram(srv.URL)
	_, err := tr.Transcribe(ctx, s.wavPath, "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}
