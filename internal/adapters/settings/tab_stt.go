package settings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/sysd"
	"github.com/partyzanex/a2text/pkg/gowhisper"
	"github.com/partyzanex/a2text/pkg/stt"
	"github.com/partyzanex/a2text/pkg/whispercpp"
)

// ModelDownloader abstracts whispercpp.Downloader so unit tests can
// supply a stub. Production wiring uses a *whispercpp.Downloader, which
// satisfies the interface structurally.
type ModelDownloader interface {
	Download(
		ctx context.Context,
		modelFile string,
		destDir string,
		progress whispercpp.ProgressFunc,
	) (string, error)
}

// buildSTTTab assembles the STT settings tab. Each logical group goes
// into a titled card so the visual structure mirrors config sections.
//
// Stores the three provider-specific cards on ff so applyProviderVisibility
// can later show only the card matching the current provider.
func (w *Window) buildSTTTab(ff *formFields) fyne.CanvasObject {
	general := rowsCard(i18n.T(i18n.KeyCardGeneral),
		formRowWithHelp(i18n.T(i18n.KeyLabelSttProvider), "help.stt_provider", ff.provider),
		formRowWithHelp(i18n.T(i18n.KeyLabelSttLanguage), "help.stt_language", ff.language),
		formRowWithHelp(i18n.T(i18n.KeyLabelUiLanguage), "help.ui_language", ff.uiLanguage),
	)

	ff.whisperCheckBtn.OnTapped = func() { w.onCheckGoWhisper(ff) }

	ff.goWhisperCard = rowsCard(i18n.T(i18n.KeyCardGoWhisper),
		formRowValidatedWithTrailingButton(i18n.T(i18n.KeyLabelUrl), "help.gw_url",
			ff.whisperURL, ff.whisperCheckBtn, ff.whisperCheckStatus,
			validateRequiredHTTPURL),
		formRowWithHelp(i18n.T(i18n.KeyLabelModel), "help.gw_model", ff.whisperModel),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelTimeout), "help.gw_timeout",
			ff.whisperTimeout, validateDuration),
		formRowWithHelp(i18n.T(i18n.KeyLabelAutoDownload), "help.gw_auto_download",
			leftAlign(ff.whisperAutoDownload)),
	)

	ff.whisperCppCard = rowsCard(i18n.T(i18n.KeyCardWhisperCpp),
		formRowWithHelp(i18n.T(i18n.KeyLabelModelsDir), "help.cpp_models_dir",
			w.buildWhisperCppModelsDirField(ff)),
		formRowWithHelp(i18n.T(i18n.KeyLabelModel), "help.cpp_model_select", ff.whisperCppModel),
		w.buildModelDownloadRow(ff),
	)

	ff.openAICard = rowsCard(i18n.T(i18n.KeyCardOpenai),
		formRowWithHelp(i18n.T(i18n.KeyLabelApiKey), "help.openai_api_key", ff.openAIAPIKey),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelBaseUrl), "help.openai_base_url",
			ff.openAIBaseURL, validateHTTPURL),
		formRowWithHelp(i18n.T(i18n.KeyLabelModel), "help.openai_model", ff.openAIModel),
	)

	balanceRow := container.NewBorder(nil, nil, nil, ff.deepgramRefresh, ff.deepgramBalance)

	ff.deepgramCard = rowsCard(i18n.T(i18n.KeyCardDeepgram),
		formRowWithHelp(i18n.T(i18n.KeyLabelApiKey), "help.deepgram_api_key", ff.deepgramAPIKey),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelBaseUrl), "help.deepgram_base_url",
			ff.deepgramBaseURL, validateHTTPURL),
		formRowWithHelp(i18n.T(i18n.KeyLabelModel), "help.deepgram_model", ff.deepgramModel),
		formRowWithHelp(i18n.T(i18n.KeyLabelDeepgramStreaming), "help.deepgram_streaming",
			leftAlign(ff.deepgramStreaming)),
		formRowWithHelp(i18n.T(i18n.KeyLabelDeepgramBalance), "help.deepgram_balance", balanceRow),
	)

	retry := rowsCard(i18n.T(i18n.KeyCardSttRetry),
		formRowWithHelp(i18n.T(i18n.KeyLabelRetryEnabled), "help.retry_enabled",
			leftAlign(ff.sttRetryEnabled)),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelRetryInitialDelay), "help.retry_initial_delay",
			ff.sttRetryInitDelay, validateDuration),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelRetryMaxDelay), "help.retry_max_delay",
			ff.sttRetryMaxDelay, validateDuration),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelRetryMaxAttempts), "help.retry_max_attempts",
			ff.sttRetryMaxAttempts, validateNonNegativeInt),
	)

	return tabBody(general, ff.goWhisperCard, ff.whisperCppCard, ff.openAICard, ff.deepgramCard, retry)
}

// buildSTTFieldWidgets creates STT-section widgets and returns an initialized formFields.
func (w *Window) buildSTTFieldWidgets() *formFields {
	openAIKeyEntry := widget.NewEntry()
	openAIKeyEntry.Password = true
	openAIKeyEntry.SetText(w.cfg.OpenAI.APIKey)

	deepgramKeyEntry := widget.NewEntry()
	deepgramKeyEntry.Password = true
	deepgramKeyEntry.SetText(w.cfg.Deepgram.APIKey)

	ff := &formFields{
		provider: widget.NewSelect(
			[]string{
				config.VoiceProviderGoWhisper,
				config.VoiceProviderWhisperCpp,
				config.VoiceProviderOpenAI,
				config.VoiceProviderDeepgram,
			},
			nil,
		),
		language:            newLanguageSelect(sttLanguageCodes()),
		uiLanguage:          newLanguageSelect(i18n.SupportedLanguages),
		whisperURL:          entryWithText(w.cfg.GoWhisper.URL, "http://localhost:9081/api/whisper"),
		whisperModel:        newModelSelectEntry(w.cfg.GoWhisper.Model),
		whisperTimeout:      entryWithText(formatDuration(w.cfg.GoWhisper.Timeout), "30s"),
		whisperAutoDownload: widget.NewCheck("", nil),
		modelPath:           newWhisperCppModelPathEntry(w.cfg.ModelPath),
		whisperCppModelsDir: entryWithText(whisperCppModelsDirOrDefault(w.cfg.WhisperCppModelsDir), ""),
		whisperCppModel:     widget.NewSelect(append([]string(nil), commonWhisperCppModels...), nil),
		modelDownloadBtn:    widget.NewButton(i18n.T(i18n.KeyButtonDownloadModel), nil),
		modelDownloadBar:    widget.NewProgressBar(),
		modelDownloadMsg:    widget.NewLabel(""),
		openAIAPIKey:        openAIKeyEntry,
		openAIBaseURL:       entryWithText(w.cfg.OpenAI.BaseURL, ""),
		openAIModel:         entryWithText(w.cfg.OpenAI.Model, "whisper-1"),
		deepgramAPIKey:      deepgramKeyEntry,
		deepgramBaseURL:     entryWithText(deepgramBaseURLOrDefault(w.cfg.Deepgram.BaseURL), stt.DeepgramDefaultBaseURL),
		deepgramModel:       newDeepgramModelSelect(w.cfg.Deepgram.Model),
		deepgramBalance:     widget.NewLabel("—"),
		deepgramRefresh:     widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), nil),
		deepgramStreaming:   widget.NewCheck("", nil),
		sttRetryEnabled:     widget.NewCheck("", nil),
		sttRetryInitDelay:   entryWithText(formatDuration(w.cfg.STTRetry.InitialDelay), "200ms"),
		sttRetryMaxDelay:    entryWithText(formatDuration(w.cfg.STTRetry.MaxDelay), "5s"),
		sttRetryMaxAttempts: entryWithText(intOrEmpty(w.cfg.STTRetry.MaxAttempts), "2"),
		whisperCheckBtn:     widget.NewButton(i18n.T(i18n.KeyButtonCheckConnection), nil),
		whisperCheckStatus:  newStatusText(),
	}

	seedWhisperCppFromModelPath(ff, w.cfg.ModelPath)
	wireWhisperCppModelChange(ff)
	w.wireDeepgramModelFetch(ff)
	w.wireDeepgramBalanceRefresh(ff)

	return ff
}

// commonDeepgramModels seeds the dropdown before a live key is supplied so
// the field is never empty. Replaced (merged) with the API-fetched list as
// soon as a valid key is entered.
//
//nolint:gochecknoglobals // immutable seed list
var commonDeepgramModels = []string{
	"nova-2",
	"nova-2-general",
	"nova-2-meeting",
	"nova-2-phonecall",
	"nova-2-voicemail",
	"enhanced",
	"base",
}

// deepgramBaseURLOrDefault returns the saved Base URL, or the canonical
// Deepgram URL when the saved value is blank. Used to prefill the entry so
// the user sees the right value out of the box instead of an empty hint.
func deepgramBaseURLOrDefault(current string) string {
	if strings.TrimSpace(current) == "" {
		return stt.DeepgramDefaultBaseURL
	}

	return current
}

// newDeepgramModelSelect builds the Deepgram model combobox seeded with
// common preset names. The list is replaced/merged once an API key is
// supplied (see wireDeepgramModelFetch).
func newDeepgramModelSelect(current string) *widget.SelectEntry {
	options := append([]string(nil), commonDeepgramModels...)
	if current != "" && !slices.Contains(options, current) {
		options = append(options, current)
	}

	entry := widget.NewSelectEntry(options)
	entry.SetPlaceHolder("nova-2")

	if current != "" {
		entry.SetText(current)
	} else {
		entry.SetText("nova-2")
	}

	return entry
}

// wireDeepgramModelFetch debounces edits to the API-key entry and refreshes
// the model dropdown by calling Deepgram's /v1/models endpoint. Errors are
// logged at debug level; on failure the existing options stay in place so
// users with a misconfigured key can still pick a model manually.
func (w *Window) wireDeepgramModelFetch(ff *formFields) {
	const debounce = 600 * time.Millisecond

	var (
		mu     sync.Mutex
		timer  *time.Timer
		cancel context.CancelFunc
	)

	trigger := func(apiKey, baseURL string) {
		mu.Lock()

		if cancel != nil {
			cancel()
		}

		ctx, c := context.WithCancel(w.rootCtx())
		cancel = c

		mu.Unlock()

		go w.refreshDeepgramModels(ctx, ff, apiKey, baseURL)
	}

	ff.deepgramAPIKey.OnChanged = func(value string) {
		w.saver.Schedule()

		key := strings.TrimSpace(value)
		if key == "" {
			return
		}

		mu.Lock()

		if timer != nil {
			timer.Stop()
		}

		baseURL := strings.TrimSpace(ff.deepgramBaseURL.Text)
		timer = time.AfterFunc(debounce, func() { trigger(key, baseURL) })

		mu.Unlock()
	}
}

// refreshDeepgramModels probes the Deepgram models endpoint and updates the
// dropdown in-place. Runs on a background goroutine; UI mutations are
// marshalled through fyne.Do. On a successful probe it also triggers a
// balance refresh — the key is known to be valid at that point.
func (w *Window) refreshDeepgramModels(
	ctx context.Context, ff *formFields, apiKey, baseURL string,
) {
	models, err := stt.FetchDeepgramModels(ctx, apiKey, baseURL)
	if err != nil {
		w.log.Debug("settings: deepgram model probe failed", slog.Any("err", err))

		return
	}

	if len(models) > 0 {
		fyne.Do(func() {
			merged := mergeDeepgramOptions(models, ff.deepgramModel.Text)
			ff.deepgramModel.SetOptions(merged)

			w.log.Debug("settings: deepgram models refreshed",
				slog.Int("count", len(models)),
			)
		})
	}

	// Key is valid → opportunistically refresh the balance caption too,
	// so the user does not have to click the manual refresh button after
	// every key change.
	w.refreshDeepgramBalance(ctx, ff, apiKey, baseURL)
}

// wireDeepgramBalanceRefresh attaches the manual-refresh handler to the
// reload icon next to the balance label. Background fetch + UI update is
// identical to the auto-refresh path.
func (w *Window) wireDeepgramBalanceRefresh(ff *formFields) {
	ff.deepgramRefresh.OnTapped = func() {
		key := strings.TrimSpace(ff.deepgramAPIKey.Text)
		if key == "" {
			ff.deepgramBalance.SetText(i18n.T(i18n.KeyBalanceNoKey))

			return
		}

		baseURL := strings.TrimSpace(ff.deepgramBaseURL.Text)
		ff.deepgramBalance.SetText(i18n.T(i18n.KeyBalanceLoading))

		go w.refreshDeepgramBalance(w.rootCtx(), ff, key, baseURL)
	}
}

// refreshDeepgramBalance pulls the project balance and updates the label.
// Errors fall back to a "—" placeholder rather than alarming the user;
// 403 is the common case (key lacks usage:read scope) and is not actionable
// from inside the app.
func (w *Window) refreshDeepgramBalance(
	ctx context.Context, ff *formFields, apiKey, baseURL string,
) {
	balances, err := stt.FetchDeepgramBalance(ctx, apiKey, baseURL)
	if err != nil {
		w.log.Debug("settings: deepgram balance probe failed", slog.Any("err", err))

		caption := i18n.T(i18n.KeyBalanceUnavailable)
		if errors.Is(err, stt.ErrDeepgramInsufficientScope) {
			caption = i18n.T(i18n.KeyBalanceNeedsScope)
		}

		fyne.Do(func() { ff.deepgramBalance.SetText(caption) })

		return
	}

	caption := stt.FormatDeepgramBalances(balances)
	if caption == "" {
		caption = i18n.T(i18n.KeyBalanceEmpty)
	}

	fyne.Do(func() {
		ff.deepgramBalance.SetText(caption)

		w.log.Debug("settings: deepgram balance refreshed",
			slog.String("caption", caption),
		)
	})
}

// mergeDeepgramOptions returns the fetched list with the currently-selected
// value appended (if missing) so the user's pre-existing choice does not
// disappear when the API returns a different set of canonical names.
func mergeDeepgramOptions(fetched []string, current string) []string {
	out := append([]string(nil), fetched...)
	current = strings.TrimSpace(current)

	if current != "" && !slices.Contains(out, current) {
		out = append(out, current)
	}

	return out
}

// seedWhisperCppFromModelPath splits cfg.ModelPath into dir + filename and
// pre-fills the models-dir entry / select so the user lands on the model
// they currently have configured.
func seedWhisperCppFromModelPath(ff *formFields, modelPath string) {
	if modelPath == "" {
		return
	}

	modelDir, modelName := filepath.Split(modelPath)
	modelDir = strings.TrimRight(modelDir, string(filepath.Separator))

	if ff.whisperCppModelsDir.Text == "" && modelDir != "" {
		ff.whisperCppModelsDir.SetText(modelDir)
	}

	if !slices.Contains(ff.whisperCppModel.Options, modelName) {
		ff.whisperCppModel.Options = append(ff.whisperCppModel.Options, modelName)
	}

	ff.whisperCppModel.SetSelected(modelName)
}

// wireWhisperCppModelChange installs the OnChanged hook that composes
// modelPath = join(models_dir, selected) — the hidden modelPath entry is
// the source of truth saved into cfg.ModelPath.
func wireWhisperCppModelChange(ff *formFields) {
	ff.whisperCppModel.OnChanged = func(name string) {
		if name == "" {
			return
		}

		dir := strings.TrimSpace(ff.whisperCppModelsDir.Text)
		if dir == "" {
			dir = whisperCppModelsDir()
		}

		if dir == "" {
			ff.modelPath.SetText(name)

			return
		}

		ff.modelPath.SetText(filepath.Join(dir, name))
	}
}

// applySTTFields writes STT-related form values back to the config.
func (w *Window) applySTTFields(ff *formFields) {
	w.cfg.Provider = ff.provider.Selected
	w.cfg.Language = ff.language.Selected
	w.cfg.UILanguage = ff.uiLanguage.Selected
	w.cfg.GoWhisper.URL = ff.whisperURL.Text
	w.cfg.GoWhisper.Model = ff.whisperModel.Text
	w.cfg.GoWhisper.Timeout = parseDuration(ff.whisperTimeout.Text)
	w.cfg.GoWhisper.AutoDownload = ff.whisperAutoDownload.Checked
	w.cfg.ModelPath = ff.modelPath.Text
	w.cfg.WhisperCppModelsDir = ff.whisperCppModelsDir.Text
	w.cfg.OpenAI.BaseURL = ff.openAIBaseURL.Text
	w.cfg.OpenAI.Model = ff.openAIModel.Text

	if ff.openAIAPIKey.Text != "" {
		w.cfg.OpenAI.APIKey = ff.openAIAPIKey.Text
	}

	w.cfg.Deepgram.BaseURL = ff.deepgramBaseURL.Text
	w.cfg.Deepgram.Model = ff.deepgramModel.Text
	w.cfg.Deepgram.Streaming = ff.deepgramStreaming.Checked

	if ff.deepgramAPIKey.Text != "" {
		w.cfg.Deepgram.APIKey = ff.deepgramAPIKey.Text
	}

	w.cfg.STTRetry.Enabled = ff.sttRetryEnabled.Checked
	w.cfg.STTRetry.InitialDelay = parseDuration(ff.sttRetryInitDelay.Text)
	w.cfg.STTRetry.MaxDelay = parseDuration(ff.sttRetryMaxDelay.Text)
	w.cfg.STTRetry.MaxAttempts = parseIntEntry(ff.sttRetryMaxAttempts.Text)
}

// applyProviderVisibility shows the STT card matching ff.provider.Selected
// and hides the others.
func applyProviderVisibility(ff *formFields) {
	cards := map[string]fyne.CanvasObject{
		config.VoiceProviderGoWhisper:  ff.goWhisperCard,
		config.VoiceProviderWhisperCpp: ff.whisperCppCard,
		config.VoiceProviderOpenAI:     ff.openAICard,
		config.VoiceProviderDeepgram:   ff.deepgramCard,
	}

	for _, card := range cards {
		if card != nil {
			card.Hide()
		}
	}

	if card := cards[ff.provider.Selected]; card != nil {
		card.Show()
	}
}

// onCheckGoWhisper handles a click on the check button: validates the
// URL syntactically before touching the network, then issues
// gowhisper.Check on a background goroutine. Returns immediately — the
// caption updates when the probe completes (or its timeout fires).
func (w *Window) onCheckGoWhisper(ff *formFields) {
	targetURL := strings.TrimSpace(ff.whisperURL.Text)
	if err := validateRequiredHTTPURL(targetURL); err != nil {
		w.log.Debug("settings: go-whisper check rejected — invalid URL",
			slog.String("url", targetURL),
			slog.Any("err", err),
		)
		setStatusText(ff.whisperCheckStatus, err.Error(), statusKindError)

		return
	}

	parent := w.rootCtx()

	const checkTimeout = 5 * time.Second

	w.log.Debug("settings: go-whisper check starting",
		slog.String("url", targetURL),
		slog.Duration("timeout", checkTimeout),
	)
	setStatusText(ff.whisperCheckStatus, i18n.T(i18n.KeyCheckStatusInProgress), statusKindNeutral)
	ff.whisperCheckBtn.Disable()

	go func() {
		defer fyne.Do(func() { ff.whisperCheckBtn.Enable() })

		res, err := gowhisper.Check(parent, targetURL, checkTimeout)

		fyne.Do(func() {
			if err != nil {
				w.log.Warn("settings: go-whisper check failed",
					slog.String("url", targetURL),
					slog.Any("err", err),
				)
				setStatusText(ff.whisperCheckStatus, i18n.T(i18n.KeyCheckStatusFailed), statusKindError)

				return
			}

			w.log.Info("settings: go-whisper check ok",
				slog.String("url", targetURL),
				slog.Int("models", len(res.Models)),
				slog.Duration("elapsed", res.Elapsed.Truncate(time.Millisecond)),
				slog.String("status", res.Status),
			)

			msg := fmt.Sprintf("%s (%d %s, %s)",
				i18n.T(i18n.KeyCheckStatusOk),
				len(res.Models), i18n.T(i18n.KeyCheckModels),
				res.Elapsed.Truncate(time.Millisecond),
			)
			setStatusText(ff.whisperCheckStatus, msg, statusKindSuccess)
		})
	}()
}

// buildModelDownloadRow assembles the "download model" row sitting just
// under the model-path entry.
func (w *Window) buildModelDownloadRow(ff *formFields) *fyne.Container {
	ff.modelDownloadBar.Hide()
	ff.modelDownloadMsg.Hide()
	ff.modelDownloadBtn.OnTapped = func() { w.onDownloadModel(ff) }

	right := container.NewBorder(nil, nil, nil, ff.modelDownloadBtn, ff.modelDownloadBar)
	stack := container.NewVBox(right, ff.modelDownloadMsg)

	return formRow(" ", stack)
}

// onDownloadModel handles a click on the "Скачать модель" button.
// Clicking the button again while a download is in progress cancels it.
func (w *Window) onDownloadModel(ff *formFields) {
	w.downloadMu.Lock()

	if w.downloadCancel != nil {
		cancel := w.downloadCancel
		w.downloadCancel = nil
		w.downloadMu.Unlock()

		cancel()

		return
	}

	current := strings.TrimSpace(ff.modelPath.Text)
	if current == "" {
		w.downloadMu.Unlock()
		w.setDownloadMessage(ff, i18n.T(i18n.KeyDownloadErrorEmptyPath), true)

		return
	}

	modelFile := filepath.Base(current)

	// Prefer the user-selected models directory; fall back to the directory
	// of the composed model path, then to the XDG default. Without this the
	// download silently lands in the XDG location while the UI shows a
	// different folder — confusing when the picked folder is on another disk.
	destDir := strings.TrimSpace(ff.whisperCppModelsDir.Text)
	if destDir == "" {
		destDir = filepath.Dir(current)
	}

	if destDir == "" || destDir == "." {
		destDir = whisperCppModelsDir()
	}

	// Fyne's OnTapped callback takes no parameters, so we cannot accept
	// a ctx here — we read it from rootCtx, which the bootstrap wires via
	// SetRootContext. contextcheck cannot follow this UI-callback path.
	parent := w.rootCtx()

	ctx, cancel := context.WithCancel(parent)
	w.downloadCancel = cancel

	if w.downloader == nil {
		w.downloader = &whispercpp.Downloader{}
	}

	dl := w.downloader

	w.downloadMu.Unlock()

	ff.modelDownloadBar.SetValue(0)
	ff.modelDownloadBar.Show()
	ff.modelDownloadBtn.SetText(i18n.T(i18n.KeyButtonDownloadCancel))
	w.setDownloadMessage(ff, i18n.T(i18n.KeyDownloadStatusStarting), false)

	w.log.Debug("settings: model download started",
		slog.String("model", modelFile),
		slog.String("dest_dir", destDir),
	)

	go w.runDownload(ctx, ff, dl, modelFile, destDir)
}

// runDownload is the goroutine body for an in-flight download. All UI
// mutations are marshalled to the Fyne goroutine via fyne.Do.
func (w *Window) runDownload(
	ctx context.Context,
	ff *formFields,
	dl ModelDownloader,
	modelFile, destDir string,
) {
	defer func() {
		w.downloadMu.Lock()
		w.downloadCancel = nil
		w.downloadMu.Unlock()

		fyne.Do(func() {
			ff.modelDownloadBar.Hide()
			ff.modelDownloadBtn.SetText(i18n.T(i18n.KeyButtonDownloadModel))
		})
	}()

	path, err := dl.Download(ctx, modelFile, destDir, func(progress whispercpp.Progress) {
		fyne.Do(func() {
			if progress.Total > 0 {
				ff.modelDownloadBar.SetValue(float64(progress.Done) / float64(progress.Total))
			}

			w.setDownloadMessage(ff, formatProgress(progress), false)
		})
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fyne.Do(func() { w.setDownloadMessage(ff, i18n.T(i18n.KeyDownloadStatusCancelled), false) })

			return
		}

		w.log.Warn("settings: model download failed", slog.Any("err", err))
		fyne.Do(func() {
			msg := fmt.Sprintf("%s: %v", i18n.T(i18n.KeyDownloadStatusFailed), err)
			w.setDownloadMessage(ff, msg, true)
		})

		return
	}

	w.log.Debug("settings: model download finished",
		slog.String("model", modelFile),
		slog.String("path", path),
	)

	fyne.Do(func() {
		ff.modelPath.SetText(path)
		w.setDownloadMessage(ff, i18n.T(i18n.KeyDownloadStatusDone), false)

		if w.saver != nil {
			w.saver.Schedule()
		}
	})
}

// setDownloadMessage updates the status caption under the download
// progress bar.
func (w *Window) setDownloadMessage(ff *formFields, text string, isError bool) {
	_ = isError

	if text == "" {
		ff.modelDownloadMsg.SetText("")
		ff.modelDownloadMsg.Hide()

		return
	}

	ff.modelDownloadMsg.SetText(text)
	ff.modelDownloadMsg.Show()
}

// formatProgress renders a Progress value as a human-readable string
// for the status caption.
func formatProgress(progress whispercpp.Progress) string {
	if progress.Total <= 0 {
		return fmt.Sprintf("%s: %s", progress.Source, formatBytes(progress.Done))
	}

	return fmt.Sprintf("%s: %s / %s",
		progress.Source, formatBytes(progress.Done), formatBytes(progress.Total))
}

// formatBytes prints a byte count with a one-letter SI suffix.
func formatBytes(n int64) string {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
	)

	switch {
	case n >= gib:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.0f KiB", float64(n)/float64(kib))
	}

	return fmt.Sprintf("%d B", n)
}

// commonGoWhisperModels lists the well-known GGML model IDs that ship with
// go-whisper.
//
//nolint:gochecknoglobals // immutable lookup table for the model combobox
var commonGoWhisperModels = []string{
	"ggml-tiny", "ggml-tiny.en",
	"ggml-base", "ggml-base.en",
	"ggml-small", "ggml-small.en",
	"ggml-medium", "ggml-medium.en",
	"ggml-large-v1",
	"ggml-large-v2",
	"ggml-large-v3",
	"ggml-large-v3-turbo",
}

// newModelSelectEntry builds the "Модель" combobox.
func newModelSelectEntry(current string) *widget.SelectEntry {
	options := make([]string, len(commonGoWhisperModels), len(commonGoWhisperModels)+1)
	copy(options, commonGoWhisperModels)

	if current != "" && !slices.Contains(options, current) {
		options = append(options, current)
	}

	entry := widget.NewSelectEntry(options)
	entry.SetText(current)
	entry.SetPlaceHolder("ggml-small")

	return entry
}

// commonWhisperCppModels lists the well-known GGML model filenames for
// whisper.cpp.
const (
	ggmlBaseBin   = "ggml-base.bin"
	ggmlSmallBin  = "ggml-small.bin"
	ggmlMediumBin = "ggml-medium.bin"
)

//nolint:gochecknoglobals // immutable lookup table for the model-path combobox
var commonWhisperCppModels = []string{
	"ggml-tiny.bin", "ggml-tiny.en.bin",
	ggmlBaseBin, "ggml-base.en.bin",
	ggmlSmallBin, "ggml-small.en.bin",
	ggmlMediumBin, "ggml-medium.en.bin",
	"ggml-large-v1.bin",
	"ggml-large-v2.bin",
	"ggml-large-v3.bin",
	"ggml-large-v3-turbo.bin",
}

// whisperCppModelsDir returns the conventional directory where users
// keep whisper.cpp .bin models on this machine. Thin wrapper around
// sysd.WhisperCppModelsDir that swallows the (rare) $HOME resolution
// error — settings UI callers want a string they can drop straight
// into an Entry, not an error to bubble up.
func whisperCppModelsDir() string {
	dir, err := sysd.WhisperCppModelsDir()
	if err != nil {
		return ""
	}

	return dir
}

// whisperCppModelsDirOrDefault returns configured when non-blank,
// otherwise the XDG-derived default from whisperCppModelsDir(). Used to
// pre-fill the settings entry so the user does not have to type the
// conventional path on a fresh install — leaving the field empty made
// the download-model button silently fall back to the same default
// anyway, but the path was invisible until the user clicked "browse".
func whisperCppModelsDirOrDefault(configured string) string {
	if dir := strings.TrimSpace(configured); dir != "" {
		return dir
	}

	return whisperCppModelsDir()
}

// scanWhisperCppModels scans a directory for available Whisper.cpp models (.bin files).
func scanWhisperCppModels(dir string) []string {
	if strings.TrimSpace(dir) == "" {
		return []string{}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}

	var models []string

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".bin") {
			models = append(models, entry.Name())
		}
	}

	slices.Sort(models)

	return models
}

// buildWhisperCppModelsDirField composes the models directory field with a folder picker button.
func (w *Window) buildWhisperCppModelsDirField(ff *formFields) *fyne.Container {
	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		w.openWhisperCppModelsDirPicker(ff)
	})

	return container.NewBorder(nil, nil, nil, browse, ff.whisperCppModelsDir)
}

// openWhisperCppModelsDirPicker shows the native folder-open dialog for Whisper.cpp models directory.
func (w *Window) openWhisperCppModelsDirPicker(ff *formFields) {
	currentPath := strings.TrimSpace(ff.whisperCppModelsDir.Text)
	if currentPath == "" {
		currentPath = os.ExpandEnv("$HOME")
	}

	selectedPath, err := tryZenity(currentPath)
	if err == nil {
		ff.whisperCppModelsDir.SetText(selectedPath)
		w.updateWhisperCppModelSelect(ff, selectedPath)

		return
	}

	selectedPath, err = tryKdialog(currentPath)
	if err == nil {
		ff.whisperCppModelsDir.SetText(selectedPath)
		w.updateWhisperCppModelSelect(ff, selectedPath)

		return
	}

	w.openFyneFolderDialogForWhisperCppModels(ff)
}

// openFyneFolderDialogForWhisperCppModels shows the Fyne folder picker for Whisper.cpp models.
func (w *Window) openFyneFolderDialogForWhisperCppModels(ff *formFields) {
	if w.win == nil {
		return
	}

	dirDialog := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			w.log.Warn("settings: folder picker failed", slog.Any("err", err))

			return
		}

		if uri == nil {
			return
		}

		path := uri.Path()
		ff.whisperCppModelsDir.SetText(path)
		w.updateWhisperCppModelSelect(ff, path)
	}, w.win)

	dirDialog.SetFilter(nil)
	dirDialog.Show()
}

// updateWhisperCppModelSelect refreshes the model select: hardcoded common
// models are always present; on-disk .bin files in dir are merged in.
func (w *Window) updateWhisperCppModelSelect(ff *formFields, dir string) {
	seen := make(map[string]struct{}, len(commonWhisperCppModels))
	options := make([]string, 0, len(commonWhisperCppModels))

	for _, name := range commonWhisperCppModels {
		seen[name] = struct{}{}

		options = append(options, name)
	}

	for _, name := range scanWhisperCppModels(dir) {
		if _, ok := seen[name]; ok {
			continue
		}

		options = append(options, name)
	}

	ff.whisperCppModel.Options = options
	if len(options) > 0 && ff.whisperCppModel.Selected == "" {
		ff.whisperCppModel.SetSelected(options[0])
	}

	ff.whisperCppModel.Refresh()
}

// newWhisperCppModelPathEntry builds the "Путь к модели" combobox.
func newWhisperCppModelPathEntry(current string) *widget.SelectEntry {
	dir := whisperCppModelsDir()

	options := make([]string, 0, len(commonWhisperCppModels)+1)
	for _, name := range commonWhisperCppModels {
		if dir == "" {
			options = append(options, name)

			continue
		}

		options = append(options, filepath.Join(dir, name))
	}

	if current != "" && !slices.Contains(options, current) {
		options = append(options, current)
	}

	entry := widget.NewSelectEntry(options)
	entry.SetText(current)

	if dir != "" {
		entry.SetPlaceHolder(filepath.Join(dir, ggmlSmallBin))
	} else {
		entry.SetPlaceHolder("/path/to/" + ggmlSmallBin)
	}

	return entry
}

// validateWhisperCppModelPath checks that a user-supplied model path
// points to an actual readable GGML/GGUF file.
func validateWhisperCppModelPath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if err := validateModelFileMeta(value); err != nil {
		return err
	}

	return validateModelFileMagic(value)
}

// validateModelFileMeta checks the stat-level properties of value.
func validateModelFileMeta(value string) error {
	const minModelSizeBytes int64 = 1 << 20 // 1 MiB

	info, err := os.Stat(value)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T(i18n.KeyValidationModelPathStatFailed), err)
	}

	if info.IsDir() {
		return errors.New(i18n.T(i18n.KeyValidationModelPathIsDir))
	}

	if !info.Mode().IsRegular() {
		return errors.New(i18n.T(i18n.KeyValidationModelPathNotRegular))
	}

	if info.Size() < minModelSizeBytes {
		return errors.New(i18n.T(i18n.KeyValidationModelPathTooSmall))
	}

	return nil
}

// validateModelFileMagic opens value, reads its first 4 bytes, and
// returns nil iff they match a known whisper-model magic.
func validateModelFileMagic(value string) (retErr error) {
	const magicSize = 4

	file, err := os.Open(filepath.Clean(value))
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T(i18n.KeyValidationModelPathOpenFailed), err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close model file: %w", closeErr)
		}
	}()

	magic := make([]byte, magicSize)
	if _, err := io.ReadFull(file, magic); err != nil {
		return fmt.Errorf("%s: %w", i18n.T(i18n.KeyValidationModelPathReadFailed), err)
	}

	if !isWhisperModelMagic(magic) {
		return errors.New(i18n.T(i18n.KeyValidationModelPathBadMagic))
	}

	return nil
}

// isWhisperModelMagic returns true when magic matches one of the file
// signatures whisper.cpp recognises.
func isWhisperModelMagic(magic []byte) bool {
	const magicLen = 4

	if len(magic) < magicLen {
		return false
	}

	switch string(magic[:magicLen]) {
	case "lmgg", "ggml", "GGUF":
		return true
	}

	return false
}

// sttLanguageAuto is the magic string written to cfg.Language to ask
// whisper to auto-detect the spoken language.
const sttLanguageAuto = "auto"

// sttLanguageCodes returns the STT-language dropdown options.
func sttLanguageCodes() []string {
	codes := make([]string, 0, 1+len(i18n.SupportedLanguages))
	codes = append(codes, sttLanguageAuto)
	codes = append(codes, i18n.SupportedLanguages...)

	return codes
}

// newLanguageSelect builds a widget.Select pre-populated with the given
// language codes.
func newLanguageSelect(codes []string) *widget.Select {
	return widget.NewSelect(codes, nil)
}

// sttLanguageOrDefault canonicalises a stored STT language value to one
// of the dropdown options.
func sttLanguageOrDefault(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return sttLanguageAuto
	}

	if slices.Contains(sttLanguageCodes(), code) {
		return code
	}

	return sttLanguageAuto
}

// uiLanguageOrDefault canonicalises a stored UI language code to one of
// the bundled locales.
func uiLanguageOrDefault(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return i18n.DefaultLanguage
	}

	if slices.Contains(i18n.SupportedLanguages, code) {
		return code
	}

	return i18n.DefaultLanguage
}
