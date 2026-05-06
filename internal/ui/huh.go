package ui

import (
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// Lipgloss color values mirroring the truecolor palette in colors.go.
// Kept here so callers can supply consistent colors to lipgloss-based
// libraries (huh, bubbletea, etc.) without duplicating the hex codes.
// AdaptiveColor lets lipgloss pick the right shade for the active
// terminal background, matching the dark/light split applied to our
// own ANSI codes.
var (
	BrandColor   = lipgloss.AdaptiveColor{Light: hexBrandLight, Dark: hexBrandDark}
	AccentColor  = lipgloss.AdaptiveColor{Light: hexAccentLight, Dark: hexAccentDark}
	InfoColor    = lipgloss.AdaptiveColor{Light: hexInfoLight, Dark: hexInfoDark}
	SuccessColor = lipgloss.AdaptiveColor{Light: hexSuccessLight, Dark: hexSuccessDark}
	WarnColor    = lipgloss.AdaptiveColor{Light: hexWarnLight, Dark: hexWarnDark}
	FailColor    = lipgloss.AdaptiveColor{Light: hexFailLight, Dark: hexFailDark}
	MutedColor   = lipgloss.AdaptiveColor{Light: hexMutedLight, Dark: hexMutedDark}
	TextColor    = lipgloss.AdaptiveColor{Light: hexTextLight, Dark: hexTextDark}
	// FocusedButtonText is the foreground used on focused buttons whose
	// background is the brand color. Stays light in both palettes so the
	// label is readable on top of the saturated brand fill.
	FocusedButtonText = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: hexTextDark}
)

// BrandTheme returns a huh form theme using the Infracost brand palette.
// Apply via `.WithTheme(ui.BrandTheme())` on any huh form. When color
// is disabled (NO_COLOR / --no-color / non-TTY) lipgloss strips the
// colors automatically, so callers don't need to branch.
func BrandTheme() *huh.Theme {
	t := huh.ThemeBase()

	// Focused (active) field.
	t.Focused.Base = t.Focused.Base.BorderForeground(BrandColor)
	t.Focused.Title = t.Focused.Title.Foreground(BrandColor).Bold(true)
	t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(BrandColor).Bold(true).MarginBottom(1)
	t.Focused.Directory = t.Focused.Directory.Foreground(BrandColor)
	t.Focused.Description = t.Focused.Description.Foreground(MutedColor)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(FailColor)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(FailColor)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(BrandColor)
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(InfoColor)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(InfoColor)
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(BrandColor)
	t.Focused.Option = t.Focused.Option.Foreground(TextColor)
	t.Focused.UnselectedOption = t.Focused.UnselectedOption.Foreground(TextColor)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(SuccessColor)
	t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(SuccessColor).SetString("✓ ")
	t.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(MutedColor).SetString("• ")
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(FocusedButtonText).Background(BrandColor)
	t.Focused.Next = t.Focused.FocusedButton
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(MutedColor)
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(InfoColor)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(BrandColor)
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(MutedColor)

	// Blurred (inactive) fields inherit focused styling minus the border.
	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()

	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description

	return t
}
