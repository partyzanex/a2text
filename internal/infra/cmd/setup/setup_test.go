package setup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// BindingHelpersSuite covers the pure binding-assembly functions.
type BindingHelpersSuite struct {
	suite.Suite
}

func TestBindingHelpersSuite(t *testing.T) {
	suite.Run(t, new(BindingHelpersSuite))
}

func (s *BindingHelpersSuite) TestBuildBinding_SuperQ() {
	got := buildBinding("Q", []string{modNameSuper})
	s.Equal("<Super>q", got)
}

func (s *BindingHelpersSuite) TestBuildBinding_CtrlShiftF12() {
	got := buildBinding("F12", []string{"ctrl", "shift"})
	s.Equal("<Control><Shift>F12", got)
}

func (s *BindingHelpersSuite) TestBuildBinding_NoModifiers() {
	got := buildBinding("F12", nil)
	s.Equal("F12", got)
}

func (s *BindingHelpersSuite) TestBuildBinding_EmptyKey_ReturnsEmpty() {
	got := buildBinding("", []string{modNameSuper})
	s.Empty(got)
}

func (s *BindingHelpersSuite) TestBuildBinding_LowercasesLetter() {
	got := buildBinding("A", nil)
	s.Equal("a", got)
}

func (s *BindingHelpersSuite) TestModifierToGNOME_Super() {
	s.Equal("<Super>", modifierToGNOME(modNameSuper))
	s.Equal("<Super>", modifierToGNOME("win"))
	s.Equal("<Super>", modifierToGNOME("meta"))
}

func (s *BindingHelpersSuite) TestModifierToGNOME_Ctrl() {
	s.Equal("<Control>", modifierToGNOME("ctrl"))
	s.Equal("<Control>", modifierToGNOME("control"))
}

func (s *BindingHelpersSuite) TestModifierToGNOME_Alt() {
	s.Equal("<Alt>", modifierToGNOME("alt"))
}

func (s *BindingHelpersSuite) TestModifierToGNOME_Shift() {
	s.Equal("<Shift>", modifierToGNOME("shift"))
}

func (s *BindingHelpersSuite) TestModifierToGNOME_CaseInsensitive() {
	s.Equal("<Super>", modifierToGNOME("SUPER"))
	s.Equal("<Control>", modifierToGNOME("Ctrl"))
}

func (s *BindingHelpersSuite) TestModifierToGNOME_Unknown_PassThrough() {
	s.Equal("Hyper", modifierToGNOME("Hyper"))
}

// GSettingsListSuite covers the list parse / format / mutate helpers.
type GSettingsListSuite struct {
	suite.Suite
}

func TestGSettingsListSuite(t *testing.T) {
	suite.Run(t, new(GSettingsListSuite))
}

func (s *GSettingsListSuite) TestParseGSettingsList_EmptyAnnotated() {
	s.Nil(parseGSettingsList("@as []"))
}

func (s *GSettingsListSuite) TestParseGSettingsList_EmptyString() {
	s.Nil(parseGSettingsList(""))
}

func (s *GSettingsListSuite) TestParseGSettingsList_SingleEntry() {
	got := parseGSettingsList("['/org/gnome/custom/a2text/']")
	s.Equal([]string{"/org/gnome/custom/a2text/"}, got)
}

func (s *GSettingsListSuite) TestParseGSettingsList_TwoEntries() {
	got := parseGSettingsList("['/org/gnome/custom/a/', '/org/gnome/custom/b/']")
	s.Equal([]string{"/org/gnome/custom/a/", "/org/gnome/custom/b/"}, got)
}

func (s *GSettingsListSuite) TestFormatGSettingsList_Empty() {
	s.Equal("@as []", formatGSettingsList(nil))
	s.Equal("@as []", formatGSettingsList([]string{}))
}

func (s *GSettingsListSuite) TestFormatGSettingsList_SingleEntry() {
	s.Equal("['/path/']", formatGSettingsList([]string{"/path/"}))
}

func (s *GSettingsListSuite) TestFormatGSettingsList_TwoEntries() {
	s.Equal("['/a/', '/b/']", formatGSettingsList([]string{"/a/", "/b/"}))
}

func (s *GSettingsListSuite) TestParseFormatRoundTrip() {
	original := "['/org/gnome/a/', '/org/gnome/b/']"
	s.Equal(original, formatGSettingsList(parseGSettingsList(original)))
}

func (s *GSettingsListSuite) TestAddToList_NewEntry() {
	paths := []string{"/existing/"}
	got := addToList(paths, "/new/")
	s.Equal([]string{"/existing/", "/new/"}, got)
}

func (s *GSettingsListSuite) TestAddToList_Idempotent() {
	paths := []string{"/existing/", "/new/"}
	got := addToList(paths, "/new/")
	s.Equal(paths, got, "must not duplicate an already-present path")
}

func (s *GSettingsListSuite) TestRemoveFromList_Exists() {
	paths := []string{"/a/", "/b/", "/c/"}
	got := removeFromList(paths, "/b/")
	s.Equal([]string{"/a/", "/c/"}, got)
}

func (s *GSettingsListSuite) TestRemoveFromList_NotPresent_NoChange() {
	paths := []string{"/a/", "/c/"}
	got := removeFromList(paths, "/b/")
	s.Equal(paths, got)
}

func (s *GSettingsListSuite) TestRemoveFromList_Empty_NoChange() {
	got := removeFromList(nil, "/b/")
	s.Empty(got)
}

// RunSetupSuite covers runSetup / runUnsetup with a fake runner.
type RunSetupSuite struct {
	suite.Suite
}

func TestRunSetupSuite(t *testing.T) {
	suite.Run(t, new(RunSetupSuite))
}

// fakeRunner records calls and returns controlled outputs.
type fakeRunner struct {
	calls  []fakeCall
	getOut map[string]string // key "schema key" → output
	err    error             // if non-nil, returned on every call
}

type fakeCall struct {
	name string
	args []string
}

func (fr *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	fr.calls = append(fr.calls, fakeCall{name: name, args: args})

	if fr.err != nil {
		return nil, fr.err
	}

	if len(args) >= 3 && args[0] == "get" {
		mapKey := args[1] + " " + args[2]
		if out, ok := fr.getOut[mapKey]; ok {
			return []byte(out + "\n"), nil
		}

		return []byte("@as []\n"), nil
	}

	return nil, nil
}

func (s *RunSetupSuite) TestRunSetup_NonGNOME_ReturnsUnsupported() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "KDE")
	s.T().Setenv("GNOME_DESKTOP_SESSION_ID", "")

	err := runSetup(s.T().Context(), s.newConfig("Q", modNameSuper), slog.New(slog.DiscardHandler), &fakeRunner{})
	s.ErrorIs(err, ErrDesktopUnsupported)
}

func (s *RunSetupSuite) TestRunSetup_EmptyKey_ReturnsMissingKey() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "GNOME")

	err := runSetup(s.T().Context(), s.newConfig(""), slog.New(slog.DiscardHandler), &fakeRunner{})
	s.ErrorIs(err, ErrMissingKey)
}

func (s *RunSetupSuite) TestRunSetup_GNOME_InvokesGsettings() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "GNOME")

	fr := &fakeRunner{}
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	err := runSetup(s.T().Context(), s.newConfig("Q", modNameSuper), log, fr)
	s.Require().NoError(err)

	// Expect 4 gsettings calls: set name, set command, set binding, then
	// one get + one set for the list.
	s.GreaterOrEqual(len(fr.calls), 5, "expected at least 5 gsettings invocations")

	for _, call := range fr.calls {
		s.Equal(gsettingsBin, call.name, "every call must target gsettings")
	}
}

func (s *RunSetupSuite) TestRunSetup_BindingAddedToExistingList() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "GNOME")

	fr := &fakeRunner{
		getOut: map[string]string{
			gnomeListSchema + " " + gnomeListKey: "['/org/gnome/custom/other/']",
		},
	}

	err := runSetup(s.T().Context(), s.newConfig("Q", modNameSuper), slog.New(slog.DiscardHandler), fr)
	s.Require().NoError(err)

	// The last call must be `gsettings set ... custom-keybindings [...]`
	// and must include both the existing path and ours.
	var listSetCall *fakeCall

	for i := range fr.calls {
		cc := &fr.calls[i]
		if cc.args[0] == "set" && cc.args[2] == gnomeListKey {
			listSetCall = cc
		}
	}

	s.Require().NotNil(listSetCall, "must have a gsettings set call for the list")
	s.Contains(listSetCall.args[3], "/org/gnome/custom/other/")
	s.Contains(listSetCall.args[3], gnomeBindingPath)
}

func (s *RunSetupSuite) TestRunSetup_RunnerError_Propagated() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "GNOME")

	fr := &fakeRunner{err: errors.New("dbus error")}

	err := runSetup(s.T().Context(), s.newConfig("Q", modNameSuper), slog.New(slog.DiscardHandler), fr)
	s.Error(err)
}

func (s *RunSetupSuite) TestRunUnsetup_NonGNOME_ReturnsUnsupported() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "XFCE")
	s.T().Setenv("GNOME_DESKTOP_SESSION_ID", "")

	err := runUnsetup(s.T().Context(), slog.New(slog.DiscardHandler), &fakeRunner{})
	s.ErrorIs(err, ErrDesktopUnsupported)
}

func (s *RunSetupSuite) TestRunUnsetup_GNOME_RemovesFromList() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "GNOME")

	fr := &fakeRunner{
		getOut: map[string]string{
			gnomeListSchema + " " + gnomeListKey: "['/org/gnome/custom/other/', '" + gnomeBindingPath + "']",
		},
	}

	err := runUnsetup(s.T().Context(), slog.New(slog.DiscardHandler), fr)
	s.Require().NoError(err)

	var listSetCall *fakeCall

	for i := range fr.calls {
		cc := &fr.calls[i]
		if cc.args[0] == "set" && cc.args[2] == gnomeListKey {
			listSetCall = cc
		}
	}

	s.Require().NotNil(listSetCall)
	s.NotContains(listSetCall.args[3], gnomeBindingPath, "our path must be removed")
	s.Contains(listSetCall.args[3], "/org/gnome/custom/other/", "other paths must be preserved")
}

// newConfig is a test helper that builds a minimal VoiceConfig with the given
// hotkey key and modifiers. Unexported method — placed after all exported
// methods to satisfy funcorder.
func (s *RunSetupSuite) newConfig(key string, mods ...string) *config.VoiceConfig {
	return &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Key:       key,
			Modifiers: mods,
		},
	}
}

// IsGNOMESuite covers the isGNOME environment detection.
type IsGNOMESuite struct {
	suite.Suite
}

func TestIsGNOMESuite(t *testing.T) {
	suite.Run(t, new(IsGNOMESuite))
}

func (s *IsGNOMESuite) TestIsGNOME_XdgGNOME() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	s.T().Setenv("GNOME_DESKTOP_SESSION_ID", "")
	s.True(isGNOME())
}

func (s *IsGNOMESuite) TestIsGNOME_XdgUbuntuGNOME() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "ubuntu:GNOME")
	s.T().Setenv("GNOME_DESKTOP_SESSION_ID", "")
	s.True(isGNOME())
}

func (s *IsGNOMESuite) TestIsGNOME_LegacyEnvVar() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "")
	s.T().Setenv("GNOME_DESKTOP_SESSION_ID", "this-is-gnome")
	s.True(isGNOME())
}

func (s *IsGNOMESuite) TestIsGNOME_KDE_ReturnsFalse() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "KDE")
	s.T().Setenv("GNOME_DESKTOP_SESSION_ID", "")
	s.False(isGNOME())
}

func (s *IsGNOMESuite) TestIsGNOME_Empty_ReturnsFalse() {
	s.T().Setenv("XDG_CURRENT_DESKTOP", "")
	s.T().Setenv("GNOME_DESKTOP_SESSION_ID", "")
	s.False(isGNOME())
}
