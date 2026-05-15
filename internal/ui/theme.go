// Package ui groups cross-cutting Fyne UI concerns (theme, shared widgets,
// icon loading) that the settings window and any future Fyne surfaces share.
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Palette colour constants packed as 0xRRGGBBAA. Defined as named consts so
// the lint config sees them as named, not magic, numbers; the hex form is
// readable and copy-pastes straight from designer specs.
const (
	colBackground  uint32 = 0x1a1b1eff
	colSurface     uint32 = 0x1e2028ff
	colInputBg     uint32 = 0x111214ff
	colAccent      uint32 = 0x4a90d4ff
	colTextPrimary uint32 = 0xe0e0e0ff
	colTextMuted   uint32 = 0x888888ff
	colBorder      uint32 = 0x2a2b2eff

	// selectionAlpha is the alpha applied to the accent colour when used
	// for hover/pressed/selection backgrounds — keeps the underlying
	// surface visible behind the tint.
	selectionAlpha uint8 = 0x40
)

// rgba unpacks a 0xRRGGBBAA literal into an NRGBA value. Inlined at the
// call sites would force mnd to flag each component; centralising the
// shift constants here gives one named-constant home for them.
func rgba(packed uint32) color.NRGBA {
	const (
		shiftR, shiftG, shiftB, shiftA = 24, 16, 8, 0
		byteMask                       = 0xff
	)

	return color.NRGBA{
		R: uint8((packed >> shiftR) & byteMask),
		G: uint8((packed >> shiftG) & byteMask),
		B: uint8((packed >> shiftB) & byteMask),
		A: uint8((packed >> shiftA) & byteMask),
	}
}

// paletteValues groups the resolved NRGBA colours. Keeping it as a struct
// (rather than scattered package-level vars) keeps gochecknoglobals happy
// with a single suppression and gives callers a tidy named API.
type paletteValues struct {
	Background, Surface, InputBg, Accent color.NRGBA
	TextPrimary, TextMuted, Border, Tint color.NRGBA
}

// Palette is the resolved theme colour table. Read-only after init.
//
//nolint:gochecknoglobals // theme palette is by design a singleton lookup table
var Palette = paletteValues{
	Background:  rgba(colBackground),
	Surface:     rgba(colSurface),
	InputBg:     rgba(colInputBg),
	Accent:      rgba(colAccent),
	TextPrimary: rgba(colTextPrimary),
	TextMuted:   rgba(colTextMuted),
	Border:      rgba(colBorder),
	Tint: color.NRGBA{
		R: rgba(colAccent).R, G: rgba(colAccent).G, B: rgba(colAccent).B, A: selectionAlpha,
	},
}

// cornerRadius is the rounding applied to inputs, selections and focused
// outlines. 8px matches the spec.
const cornerRadius float32 = 8

// compactInnerPadding shrinks the vertical room Fyne reserves between
// widgets inside layout containers (VBox, Form). Stock theme value is
// 8dp; 4dp tightens rows so a long form fits on one screen without
// looking cramped — eyeballed against the settings spec.
const compactInnerPadding float32 = 4

// rowPadding overrides the gap VBox / HBox containers put between
// their children (Fyne's SizeNamePadding). Stock theme value is 4dp,
// which packs settings rows too tightly to scan quickly; doubling it
// to 10dp gives each row visible breathing room without exploding the
// total form height.
const rowPadding float32 = 10

// A2TextTheme is the custom Fyne theme used by the settings window (and any
// future a2text Fyne surface). It overrides colours and corner radii but
// delegates fonts and icon resolution to the bundled default theme — that
// keeps text rendering and built-in icons consistent with stock Fyne.
//
// Instantiate with Theme(); the type itself is exported so that tests can
// assert against it without re-implementing the colour table.
type A2TextTheme struct {
	fallback fyne.Theme
}

// Theme returns a ready-to-use A2TextTheme. Safe for concurrent use — all
// fields are immutable.
//
//nolint:ireturn // factory: caller treats it as fyne.Theme; concrete type is private to this package
func Theme() fyne.Theme {
	return &A2TextTheme{fallback: theme.DefaultTheme()}
}

// Color resolves a theme colour. Variant (Light/Dark) is ignored — a2text
// ships a single dark palette by design; supporting two would double the
// theme surface for no visible benefit on the daemon's settings window.
//
//nolint:ireturn // fyne.Theme contract returns color.Color (interface)
func (t *A2TextTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return Palette.Background

	case theme.ColorNameForeground, theme.ColorNameForegroundOnPrimary,
		theme.ColorNameForegroundOnSuccess, theme.ColorNameForegroundOnWarning,
		theme.ColorNameForegroundOnError:
		return Palette.TextPrimary

	case theme.ColorNamePlaceHolder, theme.ColorNameDisabled:
		return Palette.TextMuted

	case theme.ColorNameInputBackground:
		return Palette.InputBg

	case theme.ColorNameInputBorder, theme.ColorNameSeparator:
		return Palette.Border

	case theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground,
		theme.ColorNameHeaderBackground:
		return Palette.Surface

	case theme.ColorNameButton, theme.ColorNameDisabledButton:
		// Plain buttons (incl. LowImportance Cancel) blend with the
		// surrounding card surface.
		return Palette.Surface

	case theme.ColorNamePrimary, theme.ColorNameFocus:
		return Palette.Accent

	case theme.ColorNameSelection, theme.ColorNameHover, theme.ColorNamePressed:
		// Selection / hover are accent-tinted but translucent so the
		// underlying surface still shows through.
		return Palette.Tint
	}

	return t.fallback.Color(name, variant)
}

// Font delegates entirely to the default theme — typography is not part
// of the spec.
//
//nolint:ireturn // fyne.Theme contract returns fyne.Resource (interface)
func (t *A2TextTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.fallback.Font(style)
}

// Icon delegates entirely to the default theme — built-in icons (errors,
// confirm/cancel inside dialogs) should keep their stock look.
//
//nolint:ireturn // fyne.Theme contract returns fyne.Resource (interface)
func (t *A2TextTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.fallback.Icon(name)
}

// Size overrides corner radii for inputs and selections to the spec'd
// 8px, shrinks SizeNameInnerPadding to compactInnerPadding so VBox-laid
// settings rows pack tightly without looking cramped, and forwards
// everything else to the default theme.
func (t *A2TextTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return cornerRadius
	case theme.SizeNameInnerPadding:
		return compactInnerPadding
	case theme.SizeNamePadding:
		return rowPadding
	}

	return t.fallback.Size(name)
}

// LabelColumnWidth is the fixed width of the right-aligned label column
// in settings rows. Exported so settings widgets can size labels uniformly.
// 270px (= 180 × 1.5) accommodates the longest Russian labels — including
// "Включить встроенный хоткей", "Модификаторы (через запятую)" and the
// "Порог тишины (dBFS)"-style multi-token captions — without truncation
// or two-line wrap.
const LabelColumnWidth float32 = 270

// SectionHeaderTextSize is the font size used for card section headers
// inside the settings window. Intentionally small (11px) — the spec calls
// for "subheadings, not h2".
const SectionHeaderTextSize float32 = 11

// SectionHeaderTracking is inserted between each character of an uppercased
// section header to approximate CSS letter-spacing. Fyne's canvas.Text has
// no native tracking control; a hair space (U+200A) between letters gives
// a tasteful, type-set look without resorting to a custom renderer.
const SectionHeaderTracking = " "

// CardCornerRadius is the rounding applied to the surface rectangle behind
// each settings card. Matches the spec'd 8px theme radius for visual
// consistency with inputs and selections.
const CardCornerRadius float32 = 8
