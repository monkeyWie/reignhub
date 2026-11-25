//go:build !darwin || ios

package nativecap

// ConfigureTextEditing leaves keyboard options to the individual input fields.
func ConfigureTextEditing() {}
