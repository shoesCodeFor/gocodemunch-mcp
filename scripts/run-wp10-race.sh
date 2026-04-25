#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="${RUN_ID:-run-$(date -u +%Y%m%dT%H%M%SZ)-wp10-race}"
OUT_DIR="${ROOT_DIR}/specs/go-migration/artifacts/race/${RUN_ID}"
RACE_REPEAT="${RACE_REPEAT:-3}"
STRESS_REPEAT="${STRESS_REPEAT:-15}"
SOAK_DURATION_SECONDS="${SOAK_DURATION_SECONDS:-0}"
WATCHER_SOAK_DURATION_SECONDS="${WATCHER_SOAK_DURATION_SECONDS:-${SOAK_DURATION_SECONDS}}"
ORCHESTRATION_SOAK_DURATION_SECONDS="${ORCHESTRATION_SOAK_DURATION_SECONDS:-${SOAK_DURATION_SECONDS}}"
INTEGRATION_SOAK_DURATION_SECONDS="${INTEGRATION_SOAK_DURATION_SECONDS:-${SOAK_DURATION_SECONDS}}"
SOAK_MIN_PER_TARGET_SECONDS="${SOAK_MIN_PER_TARGET_SECONDS:-0}"
SOAK_TEST_TIMEOUT="${SOAK_TEST_TIMEOUT:-10m}"
SOAK_PAUSE_SECONDS="${SOAK_PAUSE_SECONDS:-0}"

validate_non_negative_int() {
  local field_name="$1"
  local value="$2"
  if [[ ! "${value}" =~ ^[0-9]+$ ]]; then
    echo "${field_name} must be a non-negative integer: ${value}" >&2
    exit 2
  fi
}

validate_non_negative_int "RACE_REPEAT" "${RACE_REPEAT}"
validate_non_negative_int "STRESS_REPEAT" "${STRESS_REPEAT}"
validate_non_negative_int "SOAK_DURATION_SECONDS" "${SOAK_DURATION_SECONDS}"
validate_non_negative_int "WATCHER_SOAK_DURATION_SECONDS" "${WATCHER_SOAK_DURATION_SECONDS}"
validate_non_negative_int "ORCHESTRATION_SOAK_DURATION_SECONDS" "${ORCHESTRATION_SOAK_DURATION_SECONDS}"
validate_non_negative_int "INTEGRATION_SOAK_DURATION_SECONDS" "${INTEGRATION_SOAK_DURATION_SECONDS}"
validate_non_negative_int "SOAK_MIN_PER_TARGET_SECONDS" "${SOAK_MIN_PER_TARGET_SECONDS}"
validate_non_negative_int "SOAK_PAUSE_SECONDS" "${SOAK_PAUSE_SECONDS}"

enforce_min_soak_duration() {
  local target="$1"
  local duration_seconds="$2"
  if (( SOAK_MIN_PER_TARGET_SECONDS <= 0 )); then
    return
  fi
  if (( duration_seconds < SOAK_MIN_PER_TARGET_SECONDS )); then
    echo "${target} soak duration (${duration_seconds}s) must be >= SOAK_MIN_PER_TARGET_SECONDS (${SOAK_MIN_PER_TARGET_SECONDS}s)" >&2
    exit 2
  fi
}

enforce_min_soak_duration "watcher" "${WATCHER_SOAK_DURATION_SECONDS}"
enforce_min_soak_duration "orchestration" "${ORCHESTRATION_SOAK_DURATION_SECONDS}"
enforce_min_soak_duration "integration" "${INTEGRATION_SOAK_DURATION_SECONDS}"

mkdir -p "${OUT_DIR}"

watcher_cmd=(
  go test -race ./src/internal/watcher
  -count="${RACE_REPEAT}"
)

orchestration_cmd=(
  go test -race ./src/internal/orchestration
  -run 'TestWatcherBatchProcessorConcurrentBurstsRemainFresh|TestWatcherBatchProcessorRoutesToIndexFolder|TestRunBatchFanout|TestCallToolStrictFreshnessTimeoutSkipsBatchFanoutHandler'
  -count="${RACE_REPEAT}"
)

integration_cmd=(
  go test -race ./tests-go
  -run 'TestRelationshipToolFanoutQueueDepthAndOrdering|TestRetrievalToolFanoutQueueDepthAndOrdering'
  -count="${RACE_REPEAT}"
)

watcher_stress_cmd=(
  go test ./src/internal/watcher
  -run 'TestEmbeddedWorkerDebounceBatchesAndFiltersHiddenChanges|TestEmbeddedWorkerSingleFlightProcessesDeferredBatches|TestWaitForFreshWaitsForAllOverlappingReindexRuns'
  -count="${STRESS_REPEAT}"
)

orchestration_stress_cmd=(
  go test ./src/internal/orchestration
  -run 'TestRunBatchFanoutParallelModeAllowsConcurrentExecution|TestWatcherBatchProcessorConcurrentBurstsRemainFresh|TestRunFanoutBenchmarkToolFixtureOutlineMetrics|TestRunFanoutBenchmarkToolFixtureFindImportersMetrics|TestRunFanoutBenchmarkToolFixtureFindReferencesMetrics|TestRunFanoutBenchmarkToolFixtureCheckReferencesMetrics'
  -count="${STRESS_REPEAT}"
)

integration_stress_cmd=(
  go test ./tests-go
  -run 'TestRelationshipToolFanoutQueueDepthAndOrdering|TestRetrievalToolFanoutQueueDepthAndOrdering'
  -count="${STRESS_REPEAT}"
)

watcher_soak_cmd=(
  go test ./src/internal/watcher
  -run 'TestEmbeddedWorkerDebounceBatchesAndFiltersHiddenChanges|TestEmbeddedWorkerSingleFlightProcessesDeferredBatches|TestWaitForFreshWaitsForAllOverlappingReindexRuns'
  -count=1
  -timeout="${SOAK_TEST_TIMEOUT}"
)

orchestration_soak_cmd=(
  go test ./src/internal/orchestration
  -run 'TestRunBatchFanoutParallelModeAllowsConcurrentExecution|TestWatcherBatchProcessorConcurrentBurstsRemainFresh|TestRunFanoutBenchmarkToolFixtureOutlineMetrics|TestRunFanoutBenchmarkToolFixtureFindImportersMetrics|TestRunFanoutBenchmarkToolFixtureFindReferencesMetrics|TestRunFanoutBenchmarkToolFixtureCheckReferencesMetrics'
  -count=1
  -timeout="${SOAK_TEST_TIMEOUT}"
)

integration_soak_cmd=(
  go test ./tests-go
  -run 'TestRelationshipToolFanoutQueueDepthAndOrdering|TestRetrievalToolFanoutQueueDepthAndOrdering'
  -count=1
  -timeout="${SOAK_TEST_TIMEOUT}"
)

run_duration_soak_loop() {
  local label="$1"
  local duration_seconds="$2"
  local log_path="$3"
  shift 3
  local -a cmd=("$@")

  local started_at
  local deadline
  local now
  local iteration
  local finished_at
  local elapsed_seconds
  started_at="$(date +%s)"
  deadline=$((started_at + duration_seconds))
  iteration=0

  while true; do
    now="$(date +%s)"
    if (( now >= deadline )); then
      break
    fi

    iteration=$((iteration + 1))
    printf 'soak_target=%s iteration=%d started_at=%s\n' "${label}" "${iteration}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee -a "${log_path}"
    "${cmd[@]}" 2>&1 | tee -a "${log_path}"
    printf 'soak_target=%s iteration=%d finished_at=%s\n' "${label}" "${iteration}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee -a "${log_path}"

    if (( SOAK_PAUSE_SECONDS > 0 )); then
      sleep "${SOAK_PAUSE_SECONDS}"
    fi
  done

  if (( iteration == 0 )); then
    printf 'soak_target=%s iterations=0 note=duration-too-short-for-single-iteration\n' "${label}" | tee -a "${log_path}"
  fi
  finished_at="$(date +%s)"
  elapsed_seconds=$((finished_at - started_at))
  printf 'soak_target=%s requested_duration_seconds=%d elapsed_seconds=%d iterations=%d finished_at=%s\n' "${label}" "${duration_seconds}" "${elapsed_seconds}" "${iteration}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee -a "${log_path}"
}

(
  cd "${ROOT_DIR}"

  printf '%s\n' "${watcher_cmd[*]}" > "${OUT_DIR}/commands.txt"
  "${watcher_cmd[@]}" | tee "${OUT_DIR}/watcher-race.log"

  printf '%s\n' "${orchestration_cmd[*]}" >> "${OUT_DIR}/commands.txt"
  "${orchestration_cmd[@]}" | tee "${OUT_DIR}/orchestration-race.log"

  printf '%s\n' "${integration_cmd[*]}" >> "${OUT_DIR}/commands.txt"
  "${integration_cmd[@]}" | tee "${OUT_DIR}/integration-race.log"

  printf '%s\n' "${watcher_stress_cmd[*]}" >> "${OUT_DIR}/commands.txt"
  "${watcher_stress_cmd[@]}" | tee "${OUT_DIR}/watcher-stress.log"

  printf '%s\n' "${orchestration_stress_cmd[*]}" >> "${OUT_DIR}/commands.txt"
  "${orchestration_stress_cmd[@]}" | tee "${OUT_DIR}/orchestration-stress.log"

  printf '%s\n' "${integration_stress_cmd[*]}" >> "${OUT_DIR}/commands.txt"
  "${integration_stress_cmd[@]}" | tee "${OUT_DIR}/integration-stress.log"

  if (( WATCHER_SOAK_DURATION_SECONDS > 0 || ORCHESTRATION_SOAK_DURATION_SECONDS > 0 || INTEGRATION_SOAK_DURATION_SECONDS > 0 )); then
    {
      echo "SOAK_DURATION_SECONDS=${SOAK_DURATION_SECONDS}"
      echo "WATCHER_SOAK_DURATION_SECONDS=${WATCHER_SOAK_DURATION_SECONDS}"
      echo "ORCHESTRATION_SOAK_DURATION_SECONDS=${ORCHESTRATION_SOAK_DURATION_SECONDS}"
      echo "INTEGRATION_SOAK_DURATION_SECONDS=${INTEGRATION_SOAK_DURATION_SECONDS}"
      echo "SOAK_MIN_PER_TARGET_SECONDS=${SOAK_MIN_PER_TARGET_SECONDS}"
      echo "SOAK_TEST_TIMEOUT=${SOAK_TEST_TIMEOUT}"
      echo "SOAK_PAUSE_SECONDS=${SOAK_PAUSE_SECONDS}"
      printf '%s\n' "${watcher_soak_cmd[*]}"
      printf '%s\n' "${orchestration_soak_cmd[*]}"
      printf '%s\n' "${integration_soak_cmd[*]}"
    } >> "${OUT_DIR}/commands.txt"

    if (( WATCHER_SOAK_DURATION_SECONDS > 0 )); then
      run_duration_soak_loop "watcher" "${WATCHER_SOAK_DURATION_SECONDS}" "${OUT_DIR}/watcher-duration-soak.log" "${watcher_soak_cmd[@]}"
    fi
    if (( ORCHESTRATION_SOAK_DURATION_SECONDS > 0 )); then
      run_duration_soak_loop "orchestration" "${ORCHESTRATION_SOAK_DURATION_SECONDS}" "${OUT_DIR}/orchestration-duration-soak.log" "${orchestration_soak_cmd[@]}"
    fi
    if (( INTEGRATION_SOAK_DURATION_SECONDS > 0 )); then
      run_duration_soak_loop "integration" "${INTEGRATION_SOAK_DURATION_SECONDS}" "${OUT_DIR}/integration-duration-soak.log" "${integration_soak_cmd[@]}"
    fi
  fi
)

echo "race_run_id=${RUN_ID}"
echo "artifact_dir=${OUT_DIR}"
