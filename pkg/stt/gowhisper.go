package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/partyzanex/a2text/pkg/sttx"
)

const (
	goWhisperDefaultTimeout = 10 * time.Minute
)

// GoWhisperConfig groups the fields the GoWhisperTranscriber needs to talk to
// the go-whisper HTTP service. It is intentionally kept narrow so the adapter
// does not depend on the full application Config.
//
// BaseURL is the full base of the API including any service-specific path
// segment (e.g. "http://localhost:9081/api/whisper"). The transcriber appends
// concrete endpoints ("/model", "/transcribe") to it. Trailing slash trimmed.
type GoWhisperConfig struct {
	BaseURL      string
	Model        string        // initial model id (e.g. "ggml-small"); ".bin" suffix tolerated
	Timeout      time.Duration // HTTP client timeout, default 10 min
	AutoDownload bool          // if true, LoadModel will POST /model when missing
}

// GoWhisperTranscriber implements transcribe.Transcriber against an external
// go-whisper HTTP service (github.com/mutablelogic/go-whisper). It does not
// load any model into the local process — the service owns model state.
type GoWhisperTranscriber struct {
	httpClient   *http.Client
	log          *slog.Logger
	model        string
	baseURL      string
	mu           sync.RWMutex
	autoDownload bool
}

// NewGoWhisperTranscriber wires a transcriber against the go-whisper service.
// Defaults: Timeout=10min. log defaults to slog.Default().
//
// BaseURL is taken verbatim (trailing slash trimmed) and used as the base for
// every request; the caller is responsible for including any API path segment
// like "/api/whisper" in it.
func NewGoWhisperTranscriber(cfg GoWhisperConfig, log *slog.Logger) *GoWhisperTranscriber {
	if log == nil {
		log = slog.Default()
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = goWhisperDefaultTimeout
	}

	return &GoWhisperTranscriber{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		model:        normalizeModelID(cfg.Model),
		httpClient:   &http.Client{Timeout: timeout},
		log:          log,
		autoDownload: cfg.AutoDownload,
	}
}

// Name returns the transcriber identifier used in logs and metrics.
func (g *GoWhisperTranscriber) Name() string { return "go-whisper" }

// Transcribe uploads audioPath to the go-whisper /transcribe endpoint and
// returns the recognised text. The lang parameter is sent as the "language"
// form field unless empty or "auto" — in which case the service auto-detects.
func (g *GoWhisperTranscriber) Transcribe(ctx context.Context, audioPath, lang string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	g.mu.RLock()
	model := g.model
	g.mu.RUnlock()

	if model == "" {
		return "", fmt.Errorf("%w: model not set, call LoadModel first", sttx.ErrTranscribeFailed)
	}

	body, contentType, size, err := buildTranscribeBody(audioPath, model, lang)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.transcribeURL(), body)
	if err != nil {
		return "", fmt.Errorf("%w: create request: %w", sttx.ErrTranscribeFailed, err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	g.log.Info("go-whisper transcribe request",
		slog.String("audio", audioPath),
		slog.String("model", model),
		slog.String("lang", lang),
		slog.Int64("size_bytes", size),
	)

	start := time.Now()

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: http: %w", sttx.ErrTranscribeFailed, err)
	}
	defer g.closeBody(resp.Body)

	text, err := g.parseTranscribeResponse(resp)
	if err != nil {
		return "", err
	}

	g.log.Info("go-whisper transcribe complete",
		slog.String("audio", audioPath),
		slog.Int("result_len", len(text)),
		slog.Int64("elapsed_ms", time.Since(start).Milliseconds()),
	)

	return text, nil
}

// DetectLanguage returns "auto" — the go-whisper service auto-detects language
// per request when the language form field is omitted, so a separate detection
// call is unnecessary. Mirrors the OpenAI/Deepgram adapters.
func (g *GoWhisperTranscriber) DetectLanguage(_ context.Context, _ string) (string, error) {
	return langAuto, nil
}

// LoadModel verifies the requested model is available on the go-whisper service.
// If the model is missing and AutoDownload is true, the model is downloaded via
// POST /model and the call blocks until completion. Otherwise the method returns
// an error suggesting `make models-pull`.
//
// LoadModel uses context.Background bounded by the HTTP client timeout. Callers
// that already have a context (e.g. startup wiring) should prefer LoadModelCtx.
func (g *GoWhisperTranscriber) LoadModel(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), g.httpClient.Timeout)
	defer cancel()

	return g.LoadModelCtx(ctx, name)
}

// LoadModelCtx is the context-aware variant of LoadModel.
func (g *GoWhisperTranscriber) LoadModelCtx(ctx context.Context, name string) error {
	id := normalizeModelID(name)
	if id == "" {
		return fmt.Errorf("%w: empty model name", sttx.ErrTranscribeFailed)
	}

	if err := g.ensureModel(ctx, id); err != nil {
		return err
	}

	g.mu.Lock()
	g.model = id
	g.mu.Unlock()
	g.log.Info("go-whisper model ready", slog.String("model", id))

	return nil
}

// ReloadModel switches the active model. The go-whisper service uses a lazy
// pool, so this is implemented as a re-validation against the API plus a
// pointer swap — no real "unload" happens server-side.
func (g *GoWhisperTranscriber) ReloadModel(newName string) error {
	return g.LoadModel(newName)
}

// ActiveModel returns the currently active model ID.
func (g *GoWhisperTranscriber) ActiveModel() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.model
}

// ListModels returns the IDs of all models currently loaded on the go-whisper service.
func (g *GoWhisperTranscriber) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.modelURL(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: create request: %w", sttx.ErrTranscribeFailed, err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: http: %w", sttx.ErrTranscribeFailed, err)
	}
	defer g.closeBody(resp.Body)

	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, fmt.Errorf("%w: GET /model returned 503", sttx.ErrServiceUnavailable)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GET /model status %d: %s",
			sttx.ErrTranscribeFailed, resp.StatusCode, readErrorBody(resp.Body))
	}

	var models []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return nil, fmt.Errorf("%w: decode model list: %w", sttx.ErrTranscribeFailed, err)
	}

	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}

	return ids, nil
}

// Close is a no-op — net/http.Client does not require explicit cleanup.
func (g *GoWhisperTranscriber) Close() error { return nil }

func (g *GoWhisperTranscriber) parseTranscribeResponse(resp *http.Response) (string, error) {
	if resp.StatusCode == http.StatusServiceUnavailable {
		return "", fmt.Errorf("%w: %s", sttx.ErrServiceUnavailable, readErrorBody(resp.Body))
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d: %s", sttx.ErrTranscribeFailed, resp.StatusCode, readErrorBody(resp.Body))
	}

	var result struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Segments []struct {
			Text string `json:"text"`
		} `json:"segments"`
		Duration float64 `json:"duration"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("%w: decode response: %w", sttx.ErrTranscribeFailed, err)
	}

	text := strings.TrimSpace(result.Text)
	if text == "" {
		var sb strings.Builder
		for _, seg := range result.Segments {
			sb.WriteString(seg.Text)
		}

		text = strings.TrimSpace(sb.String())
	}

	if text == "" {
		return "", fmt.Errorf("%w: no speech detected", sttx.ErrEmptyResult)
	}

	return text, nil
}

func (g *GoWhisperTranscriber) ensureModel(ctx context.Context, id string) error {
	present, err := g.modelPresent(ctx, id)
	if err != nil {
		return err
	}

	if present {
		return nil
	}

	if !g.autoDownload {
		msg := "model %q is not loaded on go-whisper service (run `make models-pull` or set go_whisper.auto_download=true)"

		return fmt.Errorf("%w: "+msg, sttx.ErrTranscribeFailed, id)
	}

	return g.downloadModel(ctx, id+".bin")
}

func (g *GoWhisperTranscriber) modelPresent(ctx context.Context, id string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.modelURL(), http.NoBody)
	if err != nil {
		return false, fmt.Errorf("%w: create request: %w", sttx.ErrTranscribeFailed, err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: http: %w", sttx.ErrTranscribeFailed, err)
	}
	defer g.closeBody(resp.Body)

	if resp.StatusCode == http.StatusServiceUnavailable {
		return false, fmt.Errorf("%w: GET /model returned 503", sttx.ErrServiceUnavailable)
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%w: GET /model status %d: %s",
			sttx.ErrTranscribeFailed, resp.StatusCode, readErrorBody(resp.Body))
	}

	var models []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return false, fmt.Errorf("%w: decode model list: %w", sttx.ErrTranscribeFailed, err)
	}

	for _, m := range models {
		if m.ID == id {
			return true, nil
		}
	}

	return false, nil
}

func (g *GoWhisperTranscriber) downloadModel(ctx context.Context, fileName string) error {
	g.log.Info("go-whisper model download requested",
		slog.String("file", fileName),
		slog.String("url", g.modelURL()),
	)

	body := bytes.NewBufferString(fmt.Sprintf(`{"model":%q}`, fileName))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.modelURL(), body)
	if err != nil {
		return fmt.Errorf("%w: create request: %w", sttx.ErrTranscribeFailed, err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: download http: %w", sttx.ErrTranscribeFailed, err)
	}
	defer g.closeBody(resp.Body)

	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("%w: POST /model returned 503", sttx.ErrServiceUnavailable)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: POST /model status %d: %s",
			sttx.ErrTranscribeFailed, resp.StatusCode, readErrorBody(resp.Body))
	}

	g.log.Info("go-whisper model downloaded", slog.String("file", fileName))

	return nil
}

// closeBody closes a response body and logs any close error at debug level.
func (g *GoWhisperTranscriber) closeBody(body io.Closer) {
	if err := body.Close(); err != nil {
		g.log.Debug("close response body", slog.String("error", err.Error()))
	}
}

// errorBodyLimit caps how much of an error response body we include in
// wrapped error messages — protects against an unbounded server response.
const errorBodyLimit = 1 << 20 // 1 MB

// readErrorBody reads the response body for inclusion in an error message.
// Read errors are swallowed because the caller is already returning an error
// — the body is best-effort context, not load-bearing.
func readErrorBody(body io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(body, errorBodyLimit))
	if err != nil {
		return "<unreadable: " + err.Error() + ">"
	}

	return strings.TrimSpace(string(b))
}

func (g *GoWhisperTranscriber) modelURL() string      { return g.baseURL + "/model" }
func (g *GoWhisperTranscriber) transcribeURL() string { return g.baseURL + "/transcribe" }

// normalizeModelID strips a directory prefix and the ".bin" suffix from name,
// so callers can pass either "ggml-small", "ggml-small.bin" or "/data/ggml-small.bin".
func normalizeModelID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	return strings.TrimSuffix(filepath.Base(name), ".bin")
}

func buildTranscribeBody(audioPath, model, lang string) (body *bytes.Buffer, contentType string, size int64, err error) {
	if validateErr := validateWavPath(audioPath); validateErr != nil {
		return body, contentType, size, fmt.Errorf("%w: audio path: %w", validateErr, sttx.ErrTranscribeFailed)
	}

	// Safe: path validated by validateWavPath above (absolute, not symlink, regular file, .wav extension).
	file, openErr := os.Open(filepath.Clean(audioPath))
	if openErr != nil {
		return nil, "", 0, fmt.Errorf("%w: open audio: %w", sttx.ErrTranscribeFailed, openErr)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("%w: close audio: %w", sttx.ErrTranscribeFailed, closeErr)
		}
	}()

	fileInfo, statErr := file.Stat()
	if statErr != nil {
		return nil, "", 0, fmt.Errorf("%w: stat audio: %w", sttx.ErrTranscribeFailed, statErr)
	}

	var buf bytes.Buffer

	multipartWriter := multipart.NewWriter(&buf)

	if err := writeTranscribeFormFields(multipartWriter, file, audioPath, model, lang); err != nil {
		return nil, "", 0, err
	}

	if closeErr := multipartWriter.Close(); closeErr != nil {
		return nil, "", 0, fmt.Errorf("%w: close multipart: %w", sttx.ErrTranscribeFailed, closeErr)
	}

	return &buf, multipartWriter.FormDataContentType(), fileInfo.Size(), nil
}

// writeTranscribeFormFields writes the audio file, model, and optional
// language fields to the multipart writer.
func writeTranscribeFormFields(w *multipart.Writer, file *os.File, audioPath, model, lang string) error {
	part, partErr := w.CreateFormFile("audio", filepath.Base(audioPath))
	if partErr != nil {
		return fmt.Errorf("%w: form file: %w", sttx.ErrTranscribeFailed, partErr)
	}

	if _, copyErr := io.Copy(part, file); copyErr != nil {
		return fmt.Errorf("%w: copy audio: %w", sttx.ErrTranscribeFailed, copyErr)
	}

	if writeErr := w.WriteField("model", model); writeErr != nil {
		return fmt.Errorf("%w: model field: %w", sttx.ErrTranscribeFailed, writeErr)
	}

	if lang != "" && lang != langAuto {
		if writeErr := w.WriteField("language", lang); writeErr != nil {
			return fmt.Errorf("%w: language field: %w", sttx.ErrTranscribeFailed, writeErr)
		}
	}

	return nil
}
