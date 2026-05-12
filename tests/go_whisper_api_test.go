//go:build integration

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	gowhisperImage   = "ghcr.io/mutablelogic/go-whisper:latest"
	gowhisperPort    = "8081/tcp"
	containerStartup = 2 * time.Minute
	modelDownloadTTL = 10 * time.Minute
	transcribeTTL    = 5 * time.Minute

	modelTinyFile  = "ggml-tiny.bin"
	modelTinyID    = "ggml-tiny"
	modelSmallFile = "ggml-small.bin"
	modelSmallID   = "ggml-small"
)

type transcribeResponse struct {
	Task     string  `json:"task"`
	Language string  `json:"language"`
	Duration float64 `json:"duration"`
	Text     string  `json:"text"`
	Segments []struct {
		ID     int      `json:"id"`
		Start  float64  `json:"start"`
		End    float64  `json:"end"`
		Text   string   `json:"text"`
		Tokens []string `json:"tokens"`
	} `json:"segments"`
}

type GoWhisperAPISuite struct {
	suite.Suite

	container   testcontainers.Container
	baseURL     string
	httpClient  *http.Client
	testdataDir string
}

func TestGoWhisperAPISuite(t *testing.T) {
	suite.Run(t, new(GoWhisperAPISuite))
}

func (s *GoWhisperAPISuite) SetupSuite() {
	ctx := context.Background()
	s.httpClient = &http.Client{Timeout: transcribeTTL}

	wd, err := os.Getwd()
	s.Require().NoError(err)
	s.testdataDir = filepath.Join(wd, "testdata")

	modelsDir := filepath.Join(wd, ".cache", "gowhisper-models")
	s.Require().NoError(os.MkdirAll(modelsDir, 0o750))

	ctr, err := testcontainers.Run(ctx, gowhisperImage,
		testcontainers.WithExposedPorts(gowhisperPort),
		testcontainers.WithEnv(map[string]string{
			"GOWHISPER_ADDR": "0.0.0.0:8081",
			"GOWHISPER_DIR":  "/data",
		}),
		testcontainers.WithCmd("run"),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, modelsDir+":/data")
		}),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/api/whisper/model").
				WithPort(gowhisperPort).
				WithStartupTimeout(containerStartup),
		),
	)
	s.Require().NoError(err)
	s.container = ctr

	host, err := ctr.Host(ctx)
	s.Require().NoError(err)
	port, err := ctr.MappedPort(ctx, gowhisperPort)
	s.Require().NoError(err)
	s.baseURL = "http://" + net.JoinHostPort(host, port.Port())

	s.downloadModel(modelTinyFile)
	s.downloadModel(modelSmallFile)
}

func (s *GoWhisperAPISuite) TearDownSuite() {
	if s.container != nil {
		_ = s.container.Terminate(context.Background())
	}
}

// TestTranscribe_FormatMatrix verifies the API contract on jfk.{wav,mp3,ogg}
// using ggml-tiny — content check is loose, the goal is the response shape.
func (s *GoWhisperAPISuite) TestTranscribe_FormatMatrix() {
	cases := []struct {
		name string
		file string
	}{
		{name: "wav", file: "jfk.wav"},
		{name: "mp3", file: "jfk.mp3"},
		{name: "ogg", file: "jfk.ogg"},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			result := s.postTranscribe(filepath.Join(s.testdataDir, tc.file), modelTinyID, "en")

			s.Equal("transcribe", result.Task, "task field")
			s.NotEmpty(result.Language, "language field")
			s.Greater(result.Duration, 0.0, "duration must be > 0")
			s.NotEmpty(result.Text, "text must be non-empty")
			s.NotEmpty(result.Segments, "segments must be non-empty")
			s.Contains(result.Text, "country", "expected JFK quote keyword")

			for i, seg := range result.Segments {
				s.NotEmptyf(seg.Text, "segment[%d].text", i)
				s.GreaterOrEqualf(seg.End, seg.Start, "segment[%d].end >= start", i)
				s.NotEmptyf(seg.Tokens, "segment[%d].tokens", i)
			}
		})
	}
}

// TestTranscribe_Russian verifies Russian recognition with ggml-small
// (production-equivalent model). Samples from Wikimedia Commons:
// - spasibo_ru.ogg (~1s, "спасибо")
// - numbers_ru.ogg (~15s, Russian numerals 1..10).
func (s *GoWhisperAPISuite) TestTranscribe_Russian() {
	s.Run("spasibo", func() {
		result := s.postTranscribe(filepath.Join(s.testdataDir, "spasibo_ru.ogg"), modelSmallID, "ru")

		s.Equal("russian", result.Language)
		s.NotEmpty(result.Text)
		s.T().Logf("spasibo recognised: %q", result.Text)
		s.Containsf(strings.ToLower(result.Text), "спасибо", "expected greeting word")
	})

	s.Run("numbers_1_to_10", func() {
		result := s.postTranscribe(filepath.Join(s.testdataDir, "numbers_ru.ogg"), modelSmallID, "ru")

		s.Equal("russian", result.Language)
		s.NotEmpty(result.Segments)
		s.T().Logf("numbers recognised: %q", result.Text)

		// whisper нормализует русские числительные либо в кириллические слова,
		// либо в арабские цифры — принимаем любой из вариантов.
		text := strings.ToLower(result.Text)
		variants := [][]string{
			{"один", "1"},
			{"два", "2"},
			{"три", "3"},
			{"четыре", "4"},
			{"пять", "5"},
			{"шесть", "6"},
			{"семь", "7"},
			{"восемь", "8"},
			{"девять", "9"},
			{"десять", "10"},
		}
		hits := 0

		for _, alts := range variants {
			for _, alt := range alts {
				if strings.Contains(text, alt) {
					hits++

					break
				}
			}
		}

		s.GreaterOrEqualf(hits, 8, "expected at least 8 of 10 numerals to be recognised, got %d in %q", hits, result.Text)
	})
}

func (s *GoWhisperAPISuite) downloadModel(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), modelDownloadTTL)
	defer cancel()

	body := bytes.NewBufferString(fmt.Sprintf(`{"model":%q}`, name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/api/whisper/model", body)
	s.Require().NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	s.Require().NoError(err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)
	s.Require().Equalf(http.StatusOK, resp.StatusCode, "model %q download failed: %s", name, string(raw))
}

func (s *GoWhisperAPISuite) postTranscribe(path, model, language string) transcribeResponse {
	body, contentType := s.buildMultipart(path, model, language)

	ctx, cancel := context.WithTimeout(context.Background(), transcribeTTL)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/api/whisper/transcribe", body)
	s.Require().NoError(err)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	s.Require().NoError(err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)
	s.Require().Equalf(http.StatusOK, resp.StatusCode, "non-200 response: %s", string(raw))

	var result transcribeResponse
	s.Require().NoError(json.Unmarshal(raw, &result), "decode response: %s", string(raw))

	return result
}

func (s *GoWhisperAPISuite) buildMultipart(path, model, language string) (*bytes.Buffer, string) {
	file, err := os.Open(path)
	s.Require().NoError(err)

	defer func() { _ = file.Close() }()

	var buf bytes.Buffer

	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("audio", filepath.Base(path))
	s.Require().NoError(err)
	_, err = io.Copy(part, file)
	s.Require().NoError(err)

	s.Require().NoError(writer.WriteField("model", model))
	s.Require().NoError(writer.WriteField("language", language))
	s.Require().NoError(writer.Close())

	return &buf, writer.FormDataContentType()
}
