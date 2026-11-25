package provider

import (
	"context"
	"errors"
)

type ComposerChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	FullAccess  bool   `json:"fullAccess,omitempty"`
}

type ComposerControl struct {
	Kind          string           `json:"kind,omitempty"`
	ID            string           `json:"id"`
	Label         string           `json:"label"`
	Selected      string           `json:"selected,omitempty"`
	SelectedLabel string           `json:"selectedLabel,omitempty"`
	Choices       []ComposerChoice `json:"choices"`
}

type ComposerChange struct {
	Control string `json:"control"`
	Value   string `json:"value"`
	Model   string `json:"model,omitempty"`
}

type ComposerProvider interface {
	ComposerControls(context.Context, string, string) ([]ComposerControl, error)
	ConfigureComposer(context.Context, string, ComposerChange) error
}

func ValidateComposerChoice(controls []ComposerControl, change ComposerChange) error {
	for _, control := range controls {
		if control.ID == change.Control {
			for _, choice := range control.Choices {
				if choice.ID != "" && choice.ID == change.Value {
					return nil
				}
			}
		}
	}
	return errors.New("此 Agent 或模型未提供所选设置，请刷新后重试")
}
