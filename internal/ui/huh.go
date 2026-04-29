package ui

import (
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// Lipgloss color values mirroring the truecolor palette in colors.go.
// Kept here so callers can supply consistent colors to lipgloss-based
// libraries (huh, bubbletea, etc.) without duplicating the hex codes.
var (
	BrandColor   = lipgloss.Color("#6C70F2")
	AccentColor  = lipgloss.Color("#B1B8F8")
	InfoColor    = lipgloss.Color("#22D3EE")
	SuccessColor = lipgloss.Color("#10D9A0")
	WarnColor    = lipgloss.Color("#FFBA08")
	FailColor    = lipgloss.Color("#EF4444")
	MutedColor   = lipgloss.Color("#747CA2")
	TextColor    = lipgloss.Color("#EAE9F2")
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
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(TextColor).Background(BrandColor)
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
