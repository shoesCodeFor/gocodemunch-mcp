package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateRunID(t *testing.T) {
	t.Parallel()

	valid := []string{"run-20260330T120000Z", "run-alpha.1_2:3-auto"}
	for _, runID := range valid {
		if err := validateRunID(runID); err != nil {
			t.Fatalf("expected run id %q to be valid: %v", runID, err)
		}
	}

	invalid := []string{"", "run with spaces", "run-../bad", "run-foo/bar", "foo"}
	for _, runID := range invalid {
		if err := validateRunID(runID); err == nil {
			t.Fatalf("expected run id %q to be invalid", runID)
		}
	}
}

func TestMaterializeRunTemplateReplacesPlaceholders(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	templateDir := filepath.Join(root, "specs", "go-migration", "artifacts", "parity-runs", "run-id-template")
	if err := os.MkdirAll(filepath.Join(templateDir, "stubs"), 0o755); err != nil {
		t.Fatalf("create stubs dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(templateDir, "diff"), 0o755); err != nil {
		t.Fatalf("create diff dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(templateDir, "handoff"), 0o755); err != nil {
		t.Fatalf("create handoff dir: %v", err)
	}

	fixtureSource := filepath.Join(root, "specs", "go-migration", "artifacts", "parity-manifests", "parity-fixture-manifest.v1.0.0.json")
	requestSource := filepath.Join(root, "specs", "go-migration", "artifacts", "parity-manifests", "parity-request-manifest.v1.0.0.json")
	if err := os.MkdirAll(filepath.Dir(fixtureSource), 0o755); err != nil {
		t.Fatalf("create parity manifests dir: %v", err)
	}
	if err := os.WriteFile(fixtureSource, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write fixture source: %v", err)
	}
	if err := os.WriteFile(requestSource, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write request source: %v", err)
	}

	stubPath := filepath.Join(templateDir, "stubs", "stage4-go-shadow.commands.txt")
	stubContent := strings.Join([]string{
		"cd \"<go_runtime_workspace>\"",
		"MANIFEST=<run_id>",
		"FROM=<fixture_manifest_source>",
		"TO=<request_manifest_source>",
		"HARNESS=<parity_harness_workspace>",
	}, "\n") + "\n"
	if err := os.WriteFile(stubPath, []byte(stubContent), 0o644); err != nil {
		t.Fatalf("write stage stub: %v", err)
	}

	if err := os.WriteFile(filepath.Join(templateDir, "diff", "parity-gate-verdict.md"), []byte("run_id: <run_id>\n"), 0o644); err != nil {
		t.Fatalf("write verdict template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "handoff", "stage-handoffs.md"), []byte("run_id: <run_id>\n"), 0o644); err != nil {
		t.Fatalf("write handoff template: %v", err)
	}

	runID := "run-20260330T120000Z-test"
	opts, err := buildTemplateOptions(root, runID)
	if err != nil {
		t.Fatalf("build template options: %v", err)
	}

	runDir, err := materializeRunTemplate(opts)
	if err != nil {
		t.Fatalf("materialize run template: %v", err)
	}

	replacedStub, err := os.ReadFile(filepath.Join(runDir, "stubs", "stage4-go-shadow.commands.txt"))
	if err != nil {
		t.Fatalf("read replaced stub: %v", err)
	}
	stubText := string(replacedStub)
	if strings.Contains(stubText, "<run_id>") {
		t.Fatalf("expected run_id placeholder replacement, got %q", stubText)
	}
	if !strings.Contains(stubText, runID) {
		t.Fatalf("expected run id %q in stub content, got %q", runID, stubText)
	}
	if strings.Contains(stubText, "<go_runtime_workspace>") || strings.Contains(stubText, "<parity_harness_workspace>") {
		t.Fatalf("expected workspace placeholders replacement, got %q", stubText)
	}
	if !strings.Contains(stubText, fixtureSource) || !strings.Contains(stubText, requestSource) {
		t.Fatalf("expected manifest source replacements in stub, got %q", stubText)
	}

	replacedVerdict, err := os.ReadFile(filepath.Join(runDir, "diff", "parity-gate-verdict.md"))
	if err != nil {
		t.Fatalf("read replaced verdict: %v", err)
	}
	if strings.TrimSpace(string(replacedVerdict)) != "run_id: "+runID {
		t.Fatalf("unexpected verdict replacement: %q", string(replacedVerdict))
	}

	replacedHandoff, err := os.ReadFile(filepath.Join(runDir, "handoff", "stage-handoffs.md"))
	if err != nil {
		t.Fatalf("read replaced handoff: %v", err)
	}
	if strings.TrimSpace(string(replacedHandoff)) != "run_id: "+runID {
		t.Fatalf("unexpected handoff replacement: %q", string(replacedHandoff))
	}
}

func TestRenderVerdict(t *testing.T) {
	t.Parallel()

	text := renderVerdict(verdictRecord{
		RunID:                "run-20260330T120000Z-test",
		ManifestDigests:      "abc fixture; def request",
		CollectionGate:       "pass",
		PythonCapture:        "pass",
		GoCapture:            "pass",
		DiffGate:             "pass",
		BlockerCount:         0,
		MajorCount:           0,
		MinorCount:           0,
		FinalVerdict:         "approved",
		ReviewOwner:          "crusher",
		ReviewCompletedAtUTC: time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})

	checks := []string{
		"run_id: run-20260330T120000Z-test",
		"manifest_digests: abc fixture; def request",
		"collection_gate: pass",
		"python_capture: pass",
		"go_capture: pass",
		"diff_gate: pass",
		"final_verdict: approved",
		"review_owner: crusher",
	}
	for _, needle := range checks {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected verdict text to contain %q, got %q", needle, text)
		}
	}
}

func TestReadManifestDigestSummary(t *testing.T) {
	t.Parallel()

	tempFile := filepath.Join(t.TempDir(), "manifest-digests.txt")
	content := "abc  parity-fixture-manifest.json\ndef  parity-request-manifest.json\n"
	if err := os.WriteFile(tempFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write digest file: %v", err)
	}

	summary, err := readManifestDigestSummary(tempFile)
	if err != nil {
		t.Fatalf("read digest summary: %v", err)
	}
	expected := "abc  parity-fixture-manifest.json; def  parity-request-manifest.json"
	if summary != expected {
		t.Fatalf("unexpected digest summary %q (want %q)", summary, expected)
	}
}
