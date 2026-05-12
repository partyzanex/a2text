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

type OpenAISuite struct {
	suite.Suite

	wavPath string
}

func TestOpenAISuite(t *testing.T) {
	suite.Run(t, new(OpenAISuite))
}

func (s *OpenAISuite) SetupSuite() {
	tmpDir := s.T().TempDir()
	s.wavPath = filepath.Join(tmpDir, "test.wav")
	s.Require().NoError(os.WriteFile(s.wavPath, minimalWAVBytes(), 0o600))
}

// minimalWAVBytes returns a minimal valid 44-byte RIFF/WAVE header with 0 data bytes.
// Sufficient to open the file and upload to a mock server.
func minimalWAVBytes() []byte {
	b := make([]byte, 44)
	copy(b[0:], "RIFF")
	b[4], b[5], b[6], b[7] = 36, 0, 0, 0 // chunk size = 36
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	b[16], b[17], b[18], b[19] = 16, 0, 0, 0      // subchunk1 size
	b[20], b[21] = 1, 0                           // PCM
	b[22], b[23] = 1, 0                           // mono
	b[24], b[25], b[26], b[27] = 0x80, 0x3E, 0, 0 // 16000 Hz (LE)
	b[28], b[29], b[30], b[31] = 0x00, 0x7D, 0, 0 // byte rate = 32000 (LE)
	b[32], b[33] = 2, 0                           // block align
	b[34], b[35] = 16, 0                          // bits per sample
	copy(b[36:], "data")
	b[40], b[41], b[42], b[43] = 0, 0, 0, 0 // data size = 0

	return b
}

func newTestOpenAI(baseURL string) *OpenAITranscriber {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	return NewOpenAITranscriber("test-api-key", baseURL, nil, log)
}

// --- LoadModel / Close ---

func (s *OpenAISuite) TestLoadModel_IsNoop() {
	t := newTestOpenAI("http://irrelevant")
	s.NoError(t.LoadModel("/some/model.bin"))
}

func (s *OpenAISuite) TestClose_IsNoop() {
	t := newTestOpenAI("http://irrelevant")
	s.NoError(t.Close())
}

// --- Happy path ---

func (s *OpenAISuite) TestTranscribe_Happy_ReturnsText() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/v1/audio/transcriptions", r.URL.Path)
		s.Contains(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "hello world"})
	}))
	defer srv.Close()

	tr := newTestOpenAI(srv.URL)
	result, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().NoError(err)
	s.Equal("hello world", result)
}

func (s *OpenAISuite) TestTranscribe_AutoLang_NoLanguageField() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.NoError(r.ParseMultipartForm(1 << 20))
		s.Empty(r.FormValue("language"), "language field must be absent for 'auto'")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "auto result"})
	}))
	defer srv.Close()

	tr := newTestOpenAI(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "auto")
	s.NoError(err)
}

// --- Error cases ---

func (s *OpenAISuite) TestTranscribe_ServerError_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := newTestOpenAI(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "500")
}

func (s *OpenAISuite) TestTranscribe_InvalidJSON_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json{{{"))
	}))
	defer srv.Close()

	tr := newTestOpenAI(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
}

func (s *OpenAISuite) TestTranscribe_FileNotFound_ReturnsTranscribeFailed() {
	tr := newTestOpenAI("http://irrelevant")
	_, err := tr.Transcribe(context.Background(), "/nonexistent/file.wav", "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
}

func (s *OpenAISuite) TestTranscribe_ContextCanceled_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tr := newTestOpenAI(srv.URL)
	_, err := tr.Transcribe(ctx, s.wavPath, "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// --- Response body size limits ---

func (s *OpenAISuite) TestTranscribe_OversizedErrorBody_IsHandled() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(make([]byte, 2<<20)) // 2 MB
	}))
	defer srv.Close()

	tr := newTestOpenAI(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "500")
}

func (s *OpenAISuite) TestTranscribe_OversizedSuccessBody_IsHandled() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 2<<20)) // 2 MB of non-JSON
	}))
	defer srv.Close()

	tr := newTestOpenAI(srv.URL)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}
