package stt

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/pkg/sttx"
)

type GoWhisperSuite struct {
	suite.Suite

	wavPath string
}

func TestGoWhisperSuite(t *testing.T) {
	suite.Run(t, new(GoWhisperSuite))
}

func (s *GoWhisperSuite) SetupSuite() {
	tmpDir := s.T().TempDir()
	s.wavPath = filepath.Join(tmpDir, "test.wav")
	s.Require().NoError(os.WriteFile(s.wavPath, minimalWAVBytes(), 0o600))
}

func newTestGoWhisper(baseURL, model string, autoDownload bool) *GoWhisperTranscriber {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	return NewGoWhisperTranscriber(GoWhisperConfig{
		BaseURL:      baseURL,
		Model:        model,
		AutoDownload: autoDownload,
	}, log)
}

type testSegment struct {
	Text string `json:"text"`
}

type testTranscription struct {
	Task     string        `json:"task"`
	Language string        `json:"language"`
	Text     string        `json:"text"`
	Segments []testSegment `json:"segments"`
	Duration float64       `json:"duration"`
}

type testModel struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Path   string `json:"path"`
}

// writeJSON marshals v and writes it to the response. Test-only — panics on
// marshal errors so the test helpers can stay one-liners.
func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic("test json marshal: " + err.Error())
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// transcribeOK writes a minimal valid JSON transcription response.
func transcribeOK(w http.ResponseWriter, text string) {
	writeJSON(w, testTranscription{
		Task:     "transcribe",
		Language: "english",
		Duration: 1.0,
		Text:     text,
		Segments: []testSegment{{Text: text}},
	})
}

// modelList writes the GET /api/whisper/model JSON list.
func modelList(w http.ResponseWriter, ids ...string) {
	models := make([]testModel, 0, len(ids))
	for _, id := range ids {
		models = append(models, testModel{ID: id, Object: "model", Path: id + ".bin"})
	}

	writeJSON(w, models)
}

// --- Constructor / accessors ---

func (s *GoWhisperSuite) TestNew_AppliesDefaults() {
	trans := NewGoWhisperTranscriber(GoWhisperConfig{
		BaseURL: "http://localhost:8081/",
		Model:   "ggml-small.bin",
	}, nil)
	s.Equal("http://localhost:8081", trans.baseURL)
	s.Equal(goWhisperDefaultPrefix, trans.prefix)
	s.Equal("ggml-small", trans.model)
	s.Equal(goWhisperDefaultTimeout, trans.httpClient.Timeout)
}

func (s *GoWhisperSuite) TestName() {
	tr := newTestGoWhisper("http://x", "ggml-small", false)
	s.Equal("go-whisper", tr.Name())
}

func (s *GoWhisperSuite) TestClose_IsNoop() {
	tr := newTestGoWhisper("http://x", "ggml-small", false)
	s.NoError(tr.Close())
}

func (s *GoWhisperSuite) TestDetectLanguage_ReturnsAuto() {
	tr := newTestGoWhisper("http://x", "ggml-small", false)
	lang, err := tr.DetectLanguage(context.Background(), s.wavPath)
	s.Require().NoError(err)
	s.Equal(langAuto, lang)
}

func (s *GoWhisperSuite) TestNormalizeModelID() {
	s.Equal("ggml-small", normalizeModelID("ggml-small"))
	s.Equal("ggml-small", normalizeModelID("ggml-small.bin"))
	s.Equal("ggml-small", normalizeModelID("/data/ggml-small.bin"))
	s.Equal("ggml-small", normalizeModelID("  ggml-small.bin  "))
	s.Empty(normalizeModelID(""))
}

// --- Transcribe: happy paths ---

func (s *GoWhisperSuite) TestTranscribe_Happy_ParsesText() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/api/whisper/transcribe", r.URL.Path)
		s.NoError(r.ParseMultipartForm(1 << 20))
		s.Equal("ggml-small", r.FormValue("model"))
		s.Equal("ru", r.FormValue("language"))
		_, _, fileErr := r.FormFile("audio")
		s.NoError(fileErr)
		transcribeOK(w, "hello world")
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	out, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().NoError(err)
	s.Equal("hello world", out)
}

func (s *GoWhisperSuite) TestTranscribe_AutoLang_OmitsLanguageField() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.NoError(r.ParseMultipartForm(1 << 20))
		s.Empty(r.FormValue("language"), "language must be absent for 'auto'")
		transcribeOK(w, "auto detected")
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	out, err := tr.Transcribe(context.Background(), s.wavPath, langAuto)
	s.Require().NoError(err)
	s.Equal("auto detected", out)
}

func (s *GoWhisperSuite) TestTranscribe_FallsBackToSegmentsWhenTextEmpty() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, testTranscription{
			Task:     "transcribe",
			Language: "russian",
			Segments: []testSegment{{Text: "первый "}, {Text: "второй"}},
		})
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	out, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().NoError(err)
	s.Equal("первый второй", out)
}

// --- Transcribe: error paths ---

func (s *GoWhisperSuite) TestTranscribe_503_ReturnsServiceUnavailable() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model still loading", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrServiceUnavailable)
}

func (s *GoWhisperSuite) TestTranscribe_500_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "500")
	s.Contains(err.Error(), "boom")
}

func (s *GoWhisperSuite) TestTranscribe_EmptyResult_ReturnsErrEmptyResult() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		transcribeOK(w, "")
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrEmptyResult)
}

func (s *GoWhisperSuite) TestTranscribe_InvalidJSON_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
}

func (s *GoWhisperSuite) TestTranscribe_ContextCanceled_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	_, err := tr.Transcribe(ctx, s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
}

func (s *GoWhisperSuite) TestTranscribe_ModelNotSet_ReturnsTranscribeFailed() {
	tr := newTestGoWhisper("http://irrelevant", "", false)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "model not set")
}

func (s *GoWhisperSuite) TestTranscribe_FileNotFound_ReturnsTranscribeFailed() {
	tr := newTestGoWhisper("http://irrelevant", "ggml-small", false)
	_, err := tr.Transcribe(context.Background(), "/nonexistent/audio.wav", "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
}

// --- LoadModel ---

func (s *GoWhisperSuite) TestLoadModel_AlreadyPresent_Success() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal(http.MethodGet, r.Method)
		s.Equal("/api/whisper/model", r.URL.Path)
		modelList(w, "ggml-small")
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "", false)
	s.Require().NoError(tr.LoadModel("ggml-small.bin"))
	s.Equal("ggml-small", tr.model)
}

func (s *GoWhisperSuite) TestLoadModel_Missing_NoAutoDownload_Errors() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelList(w, "ggml-tiny") // does NOT contain ggml-small
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "", false)
	err := tr.LoadModel("ggml-small")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "make models-pull")
}

func (s *GoWhisperSuite) TestLoadModel_Missing_AutoDownload_Success() {
	var posted int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			modelList(w) // empty list
		case http.MethodPost:
			posted++
			body, _ := io.ReadAll(r.Body)
			s.Contains(string(body), `"ggml-small.bin"`, "POST body should reference ggml-small.bin")
			writeJSON(w, testModel{ID: "ggml-small", Object: "model", Path: "ggml-small.bin"})
		}
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "", true)
	s.Require().NoError(tr.LoadModel("ggml-small"))
	s.Equal("ggml-small", tr.model)
	s.Equal(1, posted, "POST /model должен быть вызван один раз")
}

func (s *GoWhisperSuite) TestLoadModel_503_ReturnsServiceUnavailable() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "starting up", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "", false)
	err := tr.LoadModel("ggml-small")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrServiceUnavailable)
}

func (s *GoWhisperSuite) TestLoadModel_DownloadFails_ReturnsTranscribeFailed() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			modelList(w)
		case http.MethodPost:
			http.Error(w, `{"code":404,"reason":"Not Found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "", true)
	err := tr.LoadModel("ggml-bogus")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "404")
}

func (s *GoWhisperSuite) TestLoadModel_EmptyName_Errors() {
	tr := newTestGoWhisper("http://irrelevant", "", false)
	err := tr.LoadModel("")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
}

func (s *GoWhisperSuite) TestReloadModel_UpdatesActiveModel() {
	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		modelList(w, "ggml-small", "ggml-medium")
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	s.Require().NoError(tr.ReloadModel("ggml-medium.bin"))
	s.Equal("ggml-medium", tr.model)
	s.Equal(1, calls)
}

// --- ListModels / ActiveModel ---

func (s *GoWhisperSuite) TestListModels_ReturnsAll() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelList(w, "ggml-tiny", "ggml-small", "ggml-medium")
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	models, err := tr.ListModels(context.Background())
	s.Require().NoError(err)
	s.Equal([]string{"ggml-tiny", "ggml-small", "ggml-medium"}, models)
}

func (s *GoWhisperSuite) TestListModels_Empty() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelList(w)
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	models, err := tr.ListModels(context.Background())
	s.Require().NoError(err)
	s.Empty(models)
}

func (s *GoWhisperSuite) TestListModels_503() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	_, err := tr.ListModels(context.Background())
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrServiceUnavailable)
}

func (s *GoWhisperSuite) TestActiveModel() {
	tr := newTestGoWhisper("http://irrelevant", "ggml-small.bin", false)
	s.Equal("ggml-small", tr.ActiveModel())
}

// --- Multipart layout ---

func (s *GoWhisperSuite) TestTranscribe_MultipartIncludesAudioField() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.NoError(r.ParseMultipartForm(1 << 20))
		_, hdr, err := r.FormFile("audio")
		s.NoError(err)
		s.True(strings.HasSuffix(hdr.Filename, ".wav"))
		s.Positive(hdr.Size)
		transcribeOK(w, "ok")
	}))
	defer srv.Close()

	tr := newTestGoWhisper(srv.URL, "ggml-small", false)
	_, err := tr.Transcribe(context.Background(), s.wavPath, "")
	s.Require().NoError(err)
}

// --- readErrorBody ---

func TestReadErrorBody_TruncatesAtLimit(t *testing.T) {
	// Body больше 1 MB должен быть усечён до errorBodyLimit, без OOM/таймаута.
	big := strings.Repeat("A", errorBodyLimit+1024)

	got := readErrorBody(strings.NewReader(big))

	if len(got) != errorBodyLimit {
		t.Fatalf("expected %d bytes, got %d", errorBodyLimit, len(got))
	}
}

func TestReadErrorBody_ReturnsTrimmedShortBody(t *testing.T) {
	got := readErrorBody(strings.NewReader("  oops  "))
	if got != "oops" {
		t.Fatalf("expected trimmed body, got %q", got)
	}
}
