package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/depcheck"
)

// DepCheckResult is one row of the dependency report. Severity drives
// whether a missing dependency aborts daemon startup or just logs a warning.
type DepCheckResult struct {
	Group      string // "System", "Audio", "STT", "Clipboard", "Autopaste"
	Name       string
	Detail     string // human-friendly path or status text
	InstallTip string // shown when Found == false
	Found      bool
	Optional   bool // missing optional → WARN; missing required → fatal
}

// PathLookuper abstracts exec.LookPath so the testable seam can substitute
// a fake without depending on whatever happens to live in PATH on the CI machine.
//
//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=daemon -destination=depcheck_mocks_test.go -source=depcheck.go PathLookuper
type PathLookuper interface {
	LookPath(name string) (string, error)
}

type ExecLookup struct{}

func (ExecLookup) LookPath(name string) (string, error) {
	p, err := depcheck.DefaultEnv().LookPath(name)
	if err != nil {
		return p, fmt.Errorf("depcheck: %w", err)
	}

	return p, nil
}

// FileDepCheckMode returns the depcheck CLIMode for a one-shot file path.
// WAV/WAVE files are fed to the transcriber as-is; all other formats require
// ffmpeg conversion first (ModeFileAudio).
func FileDepCheckMode(path string) depcheck.CLIMode {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".wave":
		return depcheck.ModeFileWAV
	default:
		return depcheck.ModeFileAudio
	}
}

// RunDepCheck runs all dependency probes for the given config in daemon mode.
// Each result is emitted as a structured slog line so journald + log analysis
// tools see them; w receives a single one-line human summary (e.g.
// "depcheck: 6 ok, 1 warn, 0 missing"). Pass io.Discard for w when the
// summary is unwanted (tests, headless modes); nil w is also tolerated
// (treated as io.Discard) so a wiring slip cannot panic the daemon.
//
// Returns the result list AND a "fatal-missing" flag the daemon caller uses
// to decide whether to abort startup.
//
// nil cfg yields a single fatal Result describing the wiring bug.
func RunDepCheck(ctx context.Context, cfg *config.VoiceConfig, w io.Writer, log *slog.Logger) ([]DepCheckResult, bool) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if w == nil {
		w = io.Discard
	}

	if cfg == nil {
		return RunDepCheckWith(ctx, depcheck.ModeDaemon, nil, ExecLookup{}, w, log)
	}

	return RunDepCheckWith(ctx, depcheck.ModeDaemon, cfg, ExecLookup{}, w, log)
}

// RunDepCheckWith is the testable seam: probes use the supplied pathLookuper
// instead of exec.LookPath directly. Tests call this with a fakeLookup, so it
// independently guards nil log/writer rather than trusting the public entry
// point — defence-in-depth keeps the seam safe to use anywhere.
func RunDepCheckWith(
	ctx context.Context,
	mode depcheck.CLIMode,
	cfg *config.VoiceConfig,
	lookup PathLookuper,
	w io.Writer,
	log *slog.Logger,
) ([]DepCheckResult, bool) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if w == nil {
		w = io.Discard
	}

	if lookup == nil {
		// Defence-in-depth: nil lookup would panic inside the env. Use the
		// production probe so a misconfigured caller at least gets realistic
		// results instead of a crash.
		lookup = ExecLookup{}
	}

	env := depcheck.Env{
		LookPath:            lookup.LookPath,
		StatFile:            os.Stat,
		HTTPHead:            depcheck.DefaultEnv().HTTPHead,
		WhisperCppAvailable: whisperCppAvailable,
	}

	allDeps, missing := depcheck.CheckMode(ctx, mode, cfg, env)

	return renderResults(ctx, allDeps, missing, env, w, log)
}

// depKey is a composite key for the missing-index lookup.
type depKey struct{ name, group string }

// buildDepResult constructs a single DepCheckResult row from a dependency.
func buildDepResult(
	dep *depcheck.Dependency,
	missingIdx map[depKey]struct{},
	env depcheck.Env,
	ctx context.Context,
) DepCheckResult {
	_, isMissing := missingIdx[depKey{dep.Name, dep.Group}]

	var detail string

	if !isMissing {
		detail = dep.Check(ctx, env).Detail
	}

	tip := ""
	if isMissing {
		tip = dep.InstallHint
	}

	return DepCheckResult{
		Group:      dep.Group,
		Name:       dep.Name,
		Detail:     detail,
		InstallTip: tip,
		Found:      !isMissing,
		Optional:   dep.Optional,
	}
}

// renderResults converts the raw output of depcheck.CheckMode into DepCheckResult
// rows, emits structured log lines, writes a one-line summary to w, and returns
// the slice plus the fatal-missing flag.
func renderResults(
	ctx context.Context,
	allDeps []depcheck.Dependency,
	missing []depcheck.MissingDep,
	env depcheck.Env,
	w io.Writer,
	log *slog.Logger,
) ([]DepCheckResult, bool) {
	// Build a fast set of missing dep keys so we only re-run Check for found deps.
	missingIdx := make(map[depKey]struct{}, len(missing))
	for i := range missing {
		md := &missing[i]
		missingIdx[depKey{md.Dep.Name, md.Dep.Group}] = struct{}{}
	}

	results := make([]DepCheckResult, 0, len(allDeps))

	var okCount, warnCount, missingCount int

	var fatalMissing bool

	for i := range allDeps {
		dep := &allDeps[i]
		row := buildDepResult(dep, missingIdx, env, ctx)
		logDepResult(log, row)
		results = append(results, row)

		switch {
		case row.Found:
			okCount++
		case row.Optional:
			warnCount++
		default:
			missingCount++
			fatalMissing = true
		}
	}

	if _, err := fmt.Fprintf(w,
		"depcheck: %d ok, %d warn, %d missing\n",
		okCount, warnCount, missingCount,
	); err != nil {
		_ = err
	}

	if fatalMissing {
		log.Error("voice: depcheck found missing required dependencies")
	}

	return results, fatalMissing
}

// logDepResult emits one structured line per result. Severity matches:
//   - found      ⇒ INFO
//   - optional missing ⇒ WARN
//   - required missing ⇒ ERROR (the daemon will fail to start anyway,
//     this is the diagnostic that survives in journal)
func logDepResult(log *slog.Logger, res DepCheckResult) {
	attrs := []any{
		slog.String("group", res.Group),
		slog.String("name", res.Name),
	}

	if res.Detail != "" {
		attrs = append(attrs, slog.String("detail", res.Detail))
	}

	if res.InstallTip != "" && !res.Found {
		attrs = append(attrs, slog.String("install_tip", res.InstallTip))
	}

	switch {
	case res.Found:
		log.Info("depcheck", attrs...)
	case res.Optional:
		log.Warn("depcheck (optional missing)", attrs...)
	default:
		log.Error("depcheck (required missing)", attrs...)
	}
}
