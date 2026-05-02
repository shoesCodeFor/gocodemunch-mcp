package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeTokenSavingsModesRequiresExplicitBothModes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		modes   []string
		wantErr string
	}{
		{
			name:    "empty",
			modes:   nil,
			wantErr: "modes must be non-empty",
		},
		{
			name:    "missing without mcp",
			modes:   []string{tokenSavingsModeWithMCP},
			wantErr: `must include "without_mcp"`,
		},
		{
			name:    "duplicate",
			modes:   []string{tokenSavingsModeWithMCP, tokenSavingsModeWithMCP, tokenSavingsModeWithoutMCP},
			wantErr: `duplicate mode "with_mcp"`,
		},
		{
			name:    "unsupported",
			modes:   []string{tokenSavingsModeWithMCP, "manual_review", tokenSavingsModeWithoutMCP},
			wantErr: `unsupported mode "manual_review"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := normalizeTokenSavingsModes(tc.modes)
			if err == nil {
				t.Fatalf("expected error for modes %#v", tc.modes)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestNormalizeTokenSavingsModesCanonicalizesOrder(t *testing.T) {
	t.Parallel()

	modes, err := normalizeTokenSavingsModes([]string{tokenSavingsModeWithoutMCP, tokenSavingsModeWithMCP})
	if err != nil {
		t.Fatalf("normalize token savings modes: %v", err)
	}
	if !reflect.DeepEqual(modes, tokenSavingsRequiredModes) {
		t.Fatalf("expected canonical mode order %#v, got %#v", tokenSavingsRequiredModes, modes)
	}
}
