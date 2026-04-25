package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	defaultProvisioningAttemptID = "A01"
	defaultProvisioningOptionID  = "HR-03"
	defaultReviewOwner           = "crusher"
)

var runIDPattern = regexp.MustCompile(`^run-[A-Za-z0-9._:-]+$`)

type runTemplateOptions struct {
	RootPath              string
	RunID                 string
	TemplatePath          string
	RunsBasePath          string
	GoRuntimeWorkspace    string
	HarnessWorkspace      string
	FixtureManifestSource string
	RequestManifestSource string
}

type stageScript struct {
	Name string
	Path string
	Env  []string
}

type mismatchReport struct {
	Summary struct {
		Blocker int `json:"blocker"`
		Major   int `json:"major"`
		Minor   int `json:"minor"`
	} `json:"summary"`
}

type handoffRecord struct {
	RunID          string
	Stage          string
	StageOwner     string
	NextStageOwner string
	Status         string
	StartedAtUTC   string
	CompletedAtUTC string
	BlockerSummary string
	EvidencePaths  []string
	Notes          string
}

type verdictRecord struct {
	RunID                string
	ManifestDigests      string
	CollectionGate       string
	PythonCapture        string
	GoCapture            string
	DiffGate             string
	BlockerCount         int
	MajorCount           int
	MinorCount           int
	FinalVerdict         string
	ReviewOwner          string
	ReviewCompletedAtUTC string
}

func main() {
	os.Exit(runWithArgs(os.Args[1:], os.Stdout, os.Stderr))
}

func runWithArgs(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gocodemunch-parity", flag.ContinueOnError)
	flags.SetOutput(stderr)

	runID := flags.String("run-id", generatedRunID(time.Now().UTC()), "Parity run id (must start with run-)")
	harnessCmd := flags.String("harness-cmd", "AUTO", "PARITY_HARNESS_CMD value for Stage 3 (AUTO enables discovery auto-binding)")
	attemptID := flags.String("provisioning-attempt-id", defaultProvisioningAttemptID, "Stage 3 provisioning attempt id")
	optionID := flags.String("provisioning-option-id", defaultProvisioningOptionID, "Stage 3 provisioning option id (HR-01/HR-02/HR-03)")
	reviewOwner := flags.String("review-owner", defaultReviewOwner, "Review owner for parity verdict/handoff artifacts")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	if err := validateRunID(*runID); err != nil {
		fmt.Fprintf(stderr, "invalid --run-id: %v\n", err)
		return 2
	}

	*attemptID = strings.TrimSpace(*attemptID)
	if *attemptID == "" {
		*attemptID = defaultProvisioningAttemptID
	}
	*optionID = normalizeProvisioningOption(*optionID)
	*reviewOwner = strings.TrimSpace(*reviewOwner)
	if *reviewOwner == "" {
		*reviewOwner = defaultReviewOwner
	}

	rootPath, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "resolve working directory: %v\n", err)
		return 1
	}

	templateOpts, err := buildTemplateOptions(rootPath, *runID)
	if err != nil {
		fmt.Fprintf(stderr, "build template options: %v\n", err)
		return 1
	}

	runDir, err := materializeRunTemplate(templateOpts)
	if err != nil {
		fmt.Fprintf(stderr, "materialize run template: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "run_dir=%s\n", runDir)

	start := time.Now().UTC()
	scripts := buildStageScripts(runDir, strings.TrimSpace(*harnessCmd), *attemptID, *optionID)
	if err := executeStageScripts(context.Background(), rootPath, scripts, stdout, stderr); err != nil {
		_ = writeHandoffFile(filepath.Join(runDir, "handoff", "stage-handoffs.md"), handoffRecord{
			RunID:          *runID,
			Stage:          "1-6",
			StageOwner:     *reviewOwner,
			NextStageOwner: "none",
			Status:         "blocked",
			StartedAtUTC:   start.Format(time.RFC3339),
			CompletedAtUTC: time.Now().UTC().Format(time.RFC3339),
			BlockerSummary: err.Error(),
			EvidencePaths: []string{
				"logs/stage1-pytest-collect.log",
				"logs/stage3-harness-discovery.log",
				"logs/stage3-harness-resolution.log",
				"logs/stage4-go-shadow.log",
				"logs/stage5-diff-triage.log",
				"logs/stage6-signoff-packet.log",
			},
			Notes: "automated by gocodemunch-parity",
		})
		fmt.Fprintf(stderr, "stage execution failed: %v\n", err)
		return 1
	}

	summary, err := loadMismatchSummary(filepath.Join(runDir, "diff", "parity-mismatch-report.json"))
	if err != nil {
		fmt.Fprintf(stderr, "load mismatch summary: %v\n", err)
		return 1
	}

	manifestDigests, err := readManifestDigestSummary(filepath.Join(runDir, "manifests", "manifest-digests.txt"))
	if err != nil {
		fmt.Fprintf(stderr, "read manifest digests: %v\n", err)
		return 1
	}

	finalVerdict := "approved"
	diffGate := "pass"
	status := "pass"
	blockerSummary := "none"
	if summary.Blocker > 0 || summary.Major > 0 || summary.Minor > 0 {
		finalVerdict = "rejected"
		diffGate = "fail"
		status = "blocked"
		blockerSummary = fmt.Sprintf("parity mismatches detected (blocker=%d major=%d minor=%d)", summary.Blocker, summary.Major, summary.Minor)
	}

	completed := time.Now().UTC()
	verdictPath := filepath.Join(runDir, "diff", "parity-gate-verdict.md")
	if err := os.WriteFile(verdictPath, []byte(renderVerdict(verdictRecord{
		RunID:                *runID,
		ManifestDigests:      manifestDigests,
		CollectionGate:       "pass",
		PythonCapture:        "pass",
		GoCapture:            "pass",
		DiffGate:             diffGate,
		BlockerCount:         summary.Blocker,
		MajorCount:           summary.Major,
		MinorCount:           summary.Minor,
		FinalVerdict:         finalVerdict,
		ReviewOwner:          *reviewOwner,
		ReviewCompletedAtUTC: completed.Format(time.RFC3339),
	})), 0o644); err != nil {
		fmt.Fprintf(stderr, "write verdict: %v\n", err)
		return 1
	}

	if err := writeHandoffFile(filepath.Join(runDir, "handoff", "stage-handoffs.md"), handoffRecord{
		RunID:          *runID,
		Stage:          "6",
		StageOwner:     *reviewOwner,
		NextStageOwner: "none",
		Status:         status,
		StartedAtUTC:   start.Format(time.RFC3339),
		CompletedAtUTC: completed.Format(time.RFC3339),
		BlockerSummary: blockerSummary,
		EvidencePaths: []string{
			"logs/stage1-pytest-collect.log",
			"logs/stage3-harness-discovery.log",
			"logs/stage3-harness-resolution.log",
			"logs/stage4-go-shadow.log",
			"logs/stage5-diff-triage.log",
			"logs/stage6-signoff-packet.log",
			"manifests/parity-fixture-manifest.json",
			"manifests/parity-request-manifest.json",
			"manifests/manifest-digests.txt",
			"manifests/harness-binding.lock",
			"diff/parity-mismatch-report.json",
			"diff/parity-gate-verdict.md",
			"diff/packet-file-inventory.txt",
		},
		Notes: "automated by gocodemunch-parity",
	}); err != nil {
		fmt.Fprintf(stderr, "write handoff: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "final_verdict=%s blocker_count=%d major_count=%d minor_count=%d\n", finalVerdict, summary.Blocker, summary.Major, summary.Minor)
	if status != "pass" {
		return 1
	}
	return 0
}

func generatedRunID(now time.Time) string {
	return fmt.Sprintf("run-%s-auto", now.UTC().Format("20060102T150405Z"))
}

func validateRunID(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return errors.New("run id is required")
	}
	if strings.Contains(runID, string(filepath.Separator)) || strings.Contains(runID, "\\") {
		return errors.New("run id cannot contain path separators")
	}
	if strings.Contains(runID, "..") {
		return errors.New("run id cannot contain '..'")
	}
	if !runIDPattern.MatchString(runID) {
		return errors.New("run id must match ^run-[A-Za-z0-9._:-]+$")
	}
	return nil
}

func normalizeProvisioningOption(optionID string) string {
	switch strings.TrimSpace(optionID) {
	case "HR-01", "HR-02", "HR-03":
		return strings.TrimSpace(optionID)
	default:
		return defaultProvisioningOptionID
	}
}

func buildTemplateOptions(rootPath, runID string) (runTemplateOptions, error) {
	if err := validateRunID(runID); err != nil {
		return runTemplateOptions{}, err
	}

	templatePath := filepath.Join(rootPath, "specs", "go-migration", "artifacts", "parity-runs", "run-id-template")
	if info, err := os.Stat(templatePath); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("%s is not a directory", templatePath)
		}
		return runTemplateOptions{}, fmt.Errorf("resolve run template: %w", err)
	}

	goWorkspace := filepath.Join(rootPath, "jcodemunch-mcp")
	if info, err := os.Stat(goWorkspace); err != nil || !info.IsDir() {
		goWorkspace = rootPath
	}

	return runTemplateOptions{
		RootPath:              rootPath,
		RunID:                 runID,
		TemplatePath:          templatePath,
		RunsBasePath:          filepath.Join(rootPath, "specs", "go-migration", "artifacts", "parity-runs"),
		GoRuntimeWorkspace:    goWorkspace,
		HarnessWorkspace:      rootPath,
		FixtureManifestSource: filepath.Join(rootPath, "specs", "go-migration", "artifacts", "parity-manifests", "parity-fixture-manifest.v1.0.0.json"),
		RequestManifestSource: filepath.Join(rootPath, "specs", "go-migration", "artifacts", "parity-manifests", "parity-request-manifest.v1.0.0.json"),
	}, nil
}

func materializeRunTemplate(opts runTemplateOptions) (string, error) {
	runDir := filepath.Join(opts.RunsBasePath, opts.RunID)
	if _, err := os.Stat(runDir); err == nil {
		return "", fmt.Errorf("run directory already exists: %s", runDir)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("probe run directory: %w", err)
	}

	if err := copyTree(opts.TemplatePath, runDir); err != nil {
		return "", fmt.Errorf("copy run template: %w", err)
	}

	replacements := map[string]string{
		"<run_id>":                   opts.RunID,
		"<go_runtime_workspace>":     opts.GoRuntimeWorkspace,
		"<parity_harness_workspace>": opts.HarnessWorkspace,
		"<fixture_manifest_source>":  opts.FixtureManifestSource,
		"<request_manifest_source>":  opts.RequestManifestSource,
	}

	stubDir := filepath.Join(runDir, "stubs")
	stubFiles, err := filepath.Glob(filepath.Join(stubDir, "*.commands.txt"))
	if err != nil {
		return "", fmt.Errorf("list stage stub files: %w", err)
	}
	for _, path := range stubFiles {
		if err := applyTemplateReplacements(path, replacements); err != nil {
			return "", err
		}
	}

	for _, path := range []string{
		filepath.Join(runDir, "diff", "parity-gate-verdict.md"),
		filepath.Join(runDir, "handoff", "stage-handoffs.md"),
	} {
		if err := applyTemplateReplacements(path, map[string]string{"<run_id>": opts.RunID}); err != nil {
			return "", err
		}
	}

	return runDir, nil
}

func copyTree(srcDir, dstDir string) error {
	return filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dstDir, relPath)

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(targetPath, content, info.Mode().Perm())
	})
}

func applyTemplateReplacements(path string, replacements map[string]string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read template file %s: %w", path, err)
	}

	orderedKeys := make([]string, 0, len(replacements))
	for key := range replacements {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)

	pairs := make([]string, 0, len(orderedKeys)*2)
	for _, key := range orderedKeys {
		pairs = append(pairs, key, replacements[key])
	}
	rewritten := strings.NewReplacer(pairs...).Replace(string(content))

	if err := os.WriteFile(path, []byte(rewritten), 0o644); err != nil {
		return fmt.Errorf("write template file %s: %w", path, err)
	}
	return nil
}

func buildStageScripts(runDir, harnessCmd, attemptID, optionID string) []stageScript {
	harnessCmd = strings.TrimSpace(harnessCmd)
	if harnessCmd == "" {
		harnessCmd = "AUTO"
	}

	stageBase := filepath.Join(runDir, "stubs")
	return []stageScript{
		{Name: "stage1-collection-gate", Path: filepath.Join(stageBase, "stage1-collection-gate.commands.txt")},
		{Name: "stage2-manifest-lock", Path: filepath.Join(stageBase, "stage2-manifest-lock.commands.txt")},
		{Name: "stage3-harness-discovery", Path: filepath.Join(stageBase, "stage3-harness-discovery.commands.txt")},
		{
			Name: "stage3-python-baseline",
			Path: filepath.Join(stageBase, "stage3-python-baseline.commands.txt"),
			Env: []string{
				"PARITY_HARNESS_CMD=" + harnessCmd,
				"PROVISIONING_ATTEMPT_ID=" + attemptID,
				"PROVISIONING_OPTION_ID=" + optionID,
			},
		},
		{Name: "stage4-go-shadow", Path: filepath.Join(stageBase, "stage4-go-shadow.commands.txt")},
		{Name: "stage5-diff-triage", Path: filepath.Join(stageBase, "stage5-diff-triage.commands.txt")},
		{Name: "stage6-signoff-packet", Path: filepath.Join(stageBase, "stage6-signoff-packet.commands.txt")},
	}
}

func executeStageScripts(ctx context.Context, rootPath string, scripts []stageScript, stdout, stderr io.Writer) error {
	for _, stage := range scripts {
		fmt.Fprintf(stdout, "running_stage=%s\n", stage.Name)
		cmd := exec.CommandContext(ctx, "bash", stage.Path)
		cmd.Dir = rootPath
		cmd.Env = append(os.Environ(), stage.Env...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", stage.Name, err)
		}
	}
	return nil
}

func loadMismatchSummary(path string) (struct{ Blocker, Major, Minor int }, error) {
	var out struct {
		Blocker int
		Major   int
		Minor   int
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}

	var report mismatchReport
	if err := json.Unmarshal(content, &report); err != nil {
		return out, err
	}
	out.Blocker = report.Summary.Blocker
	out.Major = report.Summary.Major
	out.Minor = report.Summary.Minor
	return out, nil
}

func readManifestDigestSummary(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts = append(parts, line)
	}
	if len(parts) == 0 {
		return "", errors.New("manifest digest file is empty")
	}
	return strings.Join(parts, "; "), nil
}

func renderVerdict(record verdictRecord) string {
	return strings.Join([]string{
		"run_id: " + record.RunID,
		"manifest_digests: " + record.ManifestDigests,
		"json_checksum_mode: warn",
		"collection_gate: " + record.CollectionGate,
		"python_capture: " + record.PythonCapture,
		"go_capture: " + record.GoCapture,
		"diff_gate: " + record.DiffGate,
		fmt.Sprintf("blocker_count: %d", record.BlockerCount),
		fmt.Sprintf("major_count: %d", record.MajorCount),
		fmt.Sprintf("minor_count: %d", record.MinorCount),
		"final_verdict: " + record.FinalVerdict,
		"review_owner: " + record.ReviewOwner,
		"review_completed_at_utc: " + record.ReviewCompletedAtUTC,
	}, "\n") + "\n"
}

func writeHandoffFile(path string, record handoffRecord) error {
	lines := []string{
		"run_id: " + record.RunID,
		"stage: " + record.Stage,
		"stage_owner: " + record.StageOwner,
		"next_stage_owner: " + record.NextStageOwner,
		"status: " + record.Status,
		"started_at_utc: " + record.StartedAtUTC,
		"completed_at_utc: " + record.CompletedAtUTC,
		"blocker_summary: " + record.BlockerSummary,
		"evidence_paths:",
	}
	for _, path := range record.EvidencePaths {
		lines = append(lines, "- "+path)
	}
	lines = append(lines, "notes: "+record.Notes)

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
