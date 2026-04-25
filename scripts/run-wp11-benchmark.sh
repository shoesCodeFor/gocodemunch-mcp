#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="${RUN_ID:-run-$(date -u +%Y%m%dT%H%M%SZ)-wp11}"
OUT_DIR="${ROOT_DIR}/specs/go-migration/artifacts/benchmarks/${RUN_ID}"

RUNS="${RUNS:-40}"
BATCH_ITEMS="${BATCH_ITEMS:-32}"
WORK_ITERATION_MATRIX="${WORK_ITERATION_MATRIX:-4000,12000,36000}"
QUEUE_DEPTH="${QUEUE_DEPTH:-256}"
MAX_ERROR_RATE="${MAX_ERROR_RATE:-0}"
MAX_P95_MS="${MAX_P95_MS:-120}"
MIN_THROUGHPUT_ITEMS_PER_SEC="${MIN_THROUGHPUT_ITEMS_PER_SEC:-100}"
PARALLEL_WORKERS="${PARALLEL_WORKERS:-4}"
MIN_THROUGHPUT_RATIO_VS_BASELINE="${MIN_THROUGHPUT_RATIO_VS_BASELINE:-1.10}"
MAX_P95_RATIO_VS_BASELINE="${MAX_P95_RATIO_VS_BASELINE:-1.25}"
MAX_ERROR_RATE_DELTA_VS_BASELINE="${MAX_ERROR_RATE_DELTA_VS_BASELINE:-0}"
RUN_TOOL_FIXTURE_PROFILE="${RUN_TOOL_FIXTURE_PROFILE:-1}"
TOOL_FIXTURE_PATH="${TOOL_FIXTURE_PATH:-${ROOT_DIR}/jcodemunch-mcp/tests/fixtures}"
OUTLINE_FIXTURE_PATH="${OUTLINE_FIXTURE_PATH:-${TOOL_FIXTURE_PATH}}"
IMPORTERS_FIXTURE_PATH="${IMPORTERS_FIXTURE_PATH:-${TOOL_FIXTURE_PATH}}"
REFERENCES_FIXTURE_PATH="${REFERENCES_FIXTURE_PATH:-${TOOL_FIXTURE_PATH}}"
CHECK_REFERENCES_FIXTURE_PATH="${CHECK_REFERENCES_FIXTURE_PATH:-${TOOL_FIXTURE_PATH}}"
OUTLINE_FIXTURE_MIN_FILE_COUNT="${OUTLINE_FIXTURE_MIN_FILE_COUNT:-0}"
IMPORTERS_FIXTURE_MIN_FILE_COUNT="${IMPORTERS_FIXTURE_MIN_FILE_COUNT:-0}"
REFERENCES_FIXTURE_MIN_FILE_COUNT="${REFERENCES_FIXTURE_MIN_FILE_COUNT:-0}"
CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT="${CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT:-0}"
TOOL_FIXTURE_RUNS="${TOOL_FIXTURE_RUNS:-${RUNS}}"
TOOL_FIXTURE_BATCH_ITEMS="${TOOL_FIXTURE_BATCH_ITEMS:-${BATCH_ITEMS}}"
TOOL_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC="${TOOL_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC:-10}"
TOOL_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE="${TOOL_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE:-0.65}"
TOOL_FIXTURE_MAX_P95_RATIO_VS_BASELINE="${TOOL_FIXTURE_MAX_P95_RATIO_VS_BASELINE:-3.50}"
TOOL_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE="${TOOL_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE:-0}"
RUN_IMPORTERS_FIXTURE_PROFILE="${RUN_IMPORTERS_FIXTURE_PROFILE:-1}"
IMPORTERS_FIXTURE_RUNS="${IMPORTERS_FIXTURE_RUNS:-${RUNS}}"
IMPORTERS_FIXTURE_BATCH_ITEMS="${IMPORTERS_FIXTURE_BATCH_ITEMS:-${BATCH_ITEMS}}"
IMPORTERS_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC="${IMPORTERS_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC:-10}"
IMPORTERS_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE="${IMPORTERS_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE:-0.65}"
IMPORTERS_FIXTURE_MAX_P95_RATIO_VS_BASELINE="${IMPORTERS_FIXTURE_MAX_P95_RATIO_VS_BASELINE:-4.50}"
IMPORTERS_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE="${IMPORTERS_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE:-0}"
RUN_REFERENCES_FIXTURE_PROFILE="${RUN_REFERENCES_FIXTURE_PROFILE:-1}"
REFERENCES_FIXTURE_RUNS="${REFERENCES_FIXTURE_RUNS:-${RUNS}}"
REFERENCES_FIXTURE_BATCH_ITEMS="${REFERENCES_FIXTURE_BATCH_ITEMS:-${BATCH_ITEMS}}"
REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC="${REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC:-10}"
REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE="${REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE:-0.65}"
REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE="${REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE:-5.00}"
REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE="${REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE:-0}"
RUN_CHECK_REFERENCES_FIXTURE_PROFILE="${RUN_CHECK_REFERENCES_FIXTURE_PROFILE:-1}"
CHECK_REFERENCES_FIXTURE_RUNS="${CHECK_REFERENCES_FIXTURE_RUNS:-${RUNS}}"
CHECK_REFERENCES_FIXTURE_BATCH_ITEMS="${CHECK_REFERENCES_FIXTURE_BATCH_ITEMS:-${BATCH_ITEMS}}"
CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC="${CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC:-10}"
CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE="${CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE:-0.80}"
CHECK_REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE="${CHECK_REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE:-2.50}"
CHECK_REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE="${CHECK_REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE:-0}"

mkdir -p "${OUT_DIR}"
IFS=',' read -r -a work_iteration_profiles <<< "${WORK_ITERATION_MATRIX}"

if [[ ${#work_iteration_profiles[@]} -eq 0 ]]; then
  echo "WORK_ITERATION_MATRIX must include at least one profile" >&2
  exit 2
fi

validate_non_negative_int() {
  local field_name="$1"
  local value="$2"
  if [[ ! "${value}" =~ ^[0-9]+$ ]]; then
    echo "${field_name} must be a non-negative integer: ${value}" >&2
    exit 2
  fi
}

validate_fixture_path() {
  local enabled="$1"
  local fixture_path="$2"
  local fixture_label="$3"
  if [[ "${enabled}" == "1" && ! -d "${fixture_path}" ]]; then
    echo "${fixture_label} must point to an existing directory when its fixture profile is enabled: ${fixture_path}" >&2
    exit 2
  fi
}

validate_fixture_file_floor() {
  local enabled="$1"
  local fixture_path="$2"
  local min_files="$3"
  local fixture_label="$4"
  if [[ "${enabled}" != "1" ]]; then
    return
  fi
  if (( min_files <= 0 )); then
    return
  fi
  local file_count
  file_count="$(find "${fixture_path}" -type f | wc -l | tr -d ' ')"
  if (( file_count < min_files )); then
    echo "${fixture_label} must contain at least ${min_files} files, got ${file_count}: ${fixture_path}" >&2
    exit 2
  fi
}

validate_non_negative_int "OUTLINE_FIXTURE_MIN_FILE_COUNT" "${OUTLINE_FIXTURE_MIN_FILE_COUNT}"
validate_non_negative_int "IMPORTERS_FIXTURE_MIN_FILE_COUNT" "${IMPORTERS_FIXTURE_MIN_FILE_COUNT}"
validate_non_negative_int "REFERENCES_FIXTURE_MIN_FILE_COUNT" "${REFERENCES_FIXTURE_MIN_FILE_COUNT}"
validate_non_negative_int "CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT" "${CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT}"

validate_fixture_path "${RUN_TOOL_FIXTURE_PROFILE}" "${OUTLINE_FIXTURE_PATH}" "OUTLINE_FIXTURE_PATH"
validate_fixture_path "${RUN_IMPORTERS_FIXTURE_PROFILE}" "${IMPORTERS_FIXTURE_PATH}" "IMPORTERS_FIXTURE_PATH"
validate_fixture_path "${RUN_REFERENCES_FIXTURE_PROFILE}" "${REFERENCES_FIXTURE_PATH}" "REFERENCES_FIXTURE_PATH"
validate_fixture_path "${RUN_CHECK_REFERENCES_FIXTURE_PROFILE}" "${CHECK_REFERENCES_FIXTURE_PATH}" "CHECK_REFERENCES_FIXTURE_PATH"
validate_fixture_file_floor "${RUN_TOOL_FIXTURE_PROFILE}" "${OUTLINE_FIXTURE_PATH}" "${OUTLINE_FIXTURE_MIN_FILE_COUNT}" "OUTLINE_FIXTURE_PATH"
validate_fixture_file_floor "${RUN_IMPORTERS_FIXTURE_PROFILE}" "${IMPORTERS_FIXTURE_PATH}" "${IMPORTERS_FIXTURE_MIN_FILE_COUNT}" "IMPORTERS_FIXTURE_PATH"
validate_fixture_file_floor "${RUN_REFERENCES_FIXTURE_PROFILE}" "${REFERENCES_FIXTURE_PATH}" "${REFERENCES_FIXTURE_MIN_FILE_COUNT}" "REFERENCES_FIXTURE_PATH"
validate_fixture_file_floor "${RUN_CHECK_REFERENCES_FIXTURE_PROFILE}" "${CHECK_REFERENCES_FIXTURE_PATH}" "${CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT}" "CHECK_REFERENCES_FIXTURE_PATH"

{
  echo "# wp11 benchmark calibration matrix"
  echo "RUN_ID=${RUN_ID}"
  echo "RUNS=${RUNS}"
  echo "BATCH_ITEMS=${BATCH_ITEMS}"
  echo "WORK_ITERATION_MATRIX=${WORK_ITERATION_MATRIX}"
  echo "QUEUE_DEPTH=${QUEUE_DEPTH}"
  echo "MAX_ERROR_RATE=${MAX_ERROR_RATE}"
  echo "MAX_P95_MS=${MAX_P95_MS}"
  echo "MIN_THROUGHPUT_ITEMS_PER_SEC=${MIN_THROUGHPUT_ITEMS_PER_SEC}"
  echo "PARALLEL_WORKERS=${PARALLEL_WORKERS}"
  echo "MIN_THROUGHPUT_RATIO_VS_BASELINE=${MIN_THROUGHPUT_RATIO_VS_BASELINE}"
  echo "MAX_P95_RATIO_VS_BASELINE=${MAX_P95_RATIO_VS_BASELINE}"
  echo "MAX_ERROR_RATE_DELTA_VS_BASELINE=${MAX_ERROR_RATE_DELTA_VS_BASELINE}"
  echo "RUN_TOOL_FIXTURE_PROFILE=${RUN_TOOL_FIXTURE_PROFILE}"
  echo "TOOL_FIXTURE_PATH=${TOOL_FIXTURE_PATH}"
  echo "OUTLINE_FIXTURE_PATH=${OUTLINE_FIXTURE_PATH}"
  echo "IMPORTERS_FIXTURE_PATH=${IMPORTERS_FIXTURE_PATH}"
  echo "REFERENCES_FIXTURE_PATH=${REFERENCES_FIXTURE_PATH}"
  echo "CHECK_REFERENCES_FIXTURE_PATH=${CHECK_REFERENCES_FIXTURE_PATH}"
  echo "OUTLINE_FIXTURE_MIN_FILE_COUNT=${OUTLINE_FIXTURE_MIN_FILE_COUNT}"
  echo "IMPORTERS_FIXTURE_MIN_FILE_COUNT=${IMPORTERS_FIXTURE_MIN_FILE_COUNT}"
  echo "REFERENCES_FIXTURE_MIN_FILE_COUNT=${REFERENCES_FIXTURE_MIN_FILE_COUNT}"
  echo "CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT=${CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT}"
  echo "TOOL_FIXTURE_RUNS=${TOOL_FIXTURE_RUNS}"
  echo "TOOL_FIXTURE_BATCH_ITEMS=${TOOL_FIXTURE_BATCH_ITEMS}"
  echo "TOOL_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC=${TOOL_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
  echo "TOOL_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE=${TOOL_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE}"
  echo "TOOL_FIXTURE_MAX_P95_RATIO_VS_BASELINE=${TOOL_FIXTURE_MAX_P95_RATIO_VS_BASELINE}"
  echo "TOOL_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE=${TOOL_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE}"
  echo "RUN_IMPORTERS_FIXTURE_PROFILE=${RUN_IMPORTERS_FIXTURE_PROFILE}"
  echo "IMPORTERS_FIXTURE_RUNS=${IMPORTERS_FIXTURE_RUNS}"
  echo "IMPORTERS_FIXTURE_BATCH_ITEMS=${IMPORTERS_FIXTURE_BATCH_ITEMS}"
  echo "IMPORTERS_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC=${IMPORTERS_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
  echo "IMPORTERS_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE=${IMPORTERS_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE}"
  echo "IMPORTERS_FIXTURE_MAX_P95_RATIO_VS_BASELINE=${IMPORTERS_FIXTURE_MAX_P95_RATIO_VS_BASELINE}"
  echo "IMPORTERS_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE=${IMPORTERS_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE}"
  echo "RUN_REFERENCES_FIXTURE_PROFILE=${RUN_REFERENCES_FIXTURE_PROFILE}"
  echo "REFERENCES_FIXTURE_RUNS=${REFERENCES_FIXTURE_RUNS}"
  echo "REFERENCES_FIXTURE_BATCH_ITEMS=${REFERENCES_FIXTURE_BATCH_ITEMS}"
  echo "REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC=${REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
  echo "REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE=${REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE}"
  echo "REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE=${REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE}"
  echo "REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE=${REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE}"
  echo "RUN_CHECK_REFERENCES_FIXTURE_PROFILE=${RUN_CHECK_REFERENCES_FIXTURE_PROFILE}"
  echo "CHECK_REFERENCES_FIXTURE_RUNS=${CHECK_REFERENCES_FIXTURE_RUNS}"
  echo "CHECK_REFERENCES_FIXTURE_BATCH_ITEMS=${CHECK_REFERENCES_FIXTURE_BATCH_ITEMS}"
  echo "CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC=${CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
  echo "CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE=${CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE}"
  echo "CHECK_REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE=${CHECK_REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE}"
  echo "CHECK_REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE=${CHECK_REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE}"
} > "${OUT_DIR}/commands.txt"

for index in "${!work_iteration_profiles[@]}"; do
  work_iterations="$(echo "${work_iteration_profiles[index]}" | xargs)"
  if [[ -z "${work_iterations}" ]]; then
    echo "WORK_ITERATION_MATRIX contains an empty profile at position $((index + 1))" >&2
    exit 2
  fi

  profile_id="$(printf "p%02d-w%s" "$((index + 1))" "${work_iterations}")"
  serial_out="${OUT_DIR}/serial-baseline-${profile_id}.json"
  serial_stdout="${OUT_DIR}/serial-baseline-${profile_id}.stdout.json"

  serial_cmd=(
    go run ./src/cmd/gocodemunch-slo-bench
    --mode serial
    --runs "${RUNS}"
    --batch-items "${BATCH_ITEMS}"
    --work-iterations "${work_iterations}"
    --max-workers 1
    --queue-depth "${QUEUE_DEPTH}"
    --max-error-rate "${MAX_ERROR_RATE}"
    --max-p95-ms "${MAX_P95_MS}"
    --min-throughput-items-per-sec "${MIN_THROUGHPUT_ITEMS_PER_SEC}"
    --out "${serial_out}"
  )

  (
    cd "${ROOT_DIR}"
    printf '%s\n' "${serial_cmd[*]}" >> "${OUT_DIR}/commands.txt"
    "${serial_cmd[@]}" | tee "${serial_stdout}"
  )

  if [[ "${RUN_PARALLEL_SHADOW:-1}" == "1" ]]; then
    parallel_out="${OUT_DIR}/parallel-shadow-${profile_id}.json"
    parallel_stdout="${OUT_DIR}/parallel-shadow-${profile_id}.stdout.json"

    parallel_cmd=(
      go run ./src/cmd/gocodemunch-slo-bench
      --mode parallel
      --runs "${RUNS}"
      --batch-items "${BATCH_ITEMS}"
      --work-iterations "${work_iterations}"
      --max-workers "${PARALLEL_WORKERS}"
      --queue-depth "${QUEUE_DEPTH}"
      --max-error-rate "${MAX_ERROR_RATE}"
      --max-p95-ms "${MAX_P95_MS}"
      --min-throughput-items-per-sec "${MIN_THROUGHPUT_ITEMS_PER_SEC}"
      --baseline-report "${serial_out}"
      --min-throughput-ratio-vs-baseline "${MIN_THROUGHPUT_RATIO_VS_BASELINE}"
      --max-p95-ratio-vs-baseline "${MAX_P95_RATIO_VS_BASELINE}"
      --max-error-rate-delta-vs-baseline "${MAX_ERROR_RATE_DELTA_VS_BASELINE}"
      --out "${parallel_out}"
    )

    (
      cd "${ROOT_DIR}"
      printf '%s\n' "${parallel_cmd[*]}" >> "${OUT_DIR}/commands.txt"
      "${parallel_cmd[@]}" | tee "${parallel_stdout}"
    )
  fi
done

if [[ "${RUN_TOOL_FIXTURE_PROFILE}" == "1" ]]; then
  profile_id="tool-fixture-outline"
  serial_out="${OUT_DIR}/serial-baseline-${profile_id}.json"
  serial_stdout="${OUT_DIR}/serial-baseline-${profile_id}.stdout.json"

  serial_cmd=(
    go run ./src/cmd/gocodemunch-slo-bench
    --mode serial
    --workload tool_get_file_outline_fixture
    --fixture-path "${OUTLINE_FIXTURE_PATH}"
    --min-fixture-file-count "${OUTLINE_FIXTURE_MIN_FILE_COUNT}"
    --runs "${TOOL_FIXTURE_RUNS}"
    --batch-items "${TOOL_FIXTURE_BATCH_ITEMS}"
    --max-workers 1
    --queue-depth "${QUEUE_DEPTH}"
    --max-error-rate "${MAX_ERROR_RATE}"
    --max-p95-ms "${MAX_P95_MS}"
    --min-throughput-items-per-sec "${TOOL_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
    --out "${serial_out}"
  )

  (
    cd "${ROOT_DIR}"
    printf '%s\n' "${serial_cmd[*]}" >> "${OUT_DIR}/commands.txt"
    "${serial_cmd[@]}" | tee "${serial_stdout}"
  )

  if [[ "${RUN_PARALLEL_SHADOW:-1}" == "1" ]]; then
    parallel_out="${OUT_DIR}/parallel-shadow-${profile_id}.json"
    parallel_stdout="${OUT_DIR}/parallel-shadow-${profile_id}.stdout.json"

    parallel_cmd=(
      go run ./src/cmd/gocodemunch-slo-bench
      --mode parallel
      --workload tool_get_file_outline_fixture
      --fixture-path "${OUTLINE_FIXTURE_PATH}"
      --min-fixture-file-count "${OUTLINE_FIXTURE_MIN_FILE_COUNT}"
      --runs "${TOOL_FIXTURE_RUNS}"
      --batch-items "${TOOL_FIXTURE_BATCH_ITEMS}"
      --max-workers "${PARALLEL_WORKERS}"
      --queue-depth "${QUEUE_DEPTH}"
      --max-error-rate "${MAX_ERROR_RATE}"
      --max-p95-ms "${MAX_P95_MS}"
      --min-throughput-items-per-sec "${TOOL_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
      --baseline-report "${serial_out}"
      --min-throughput-ratio-vs-baseline "${TOOL_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE}"
      --max-p95-ratio-vs-baseline "${TOOL_FIXTURE_MAX_P95_RATIO_VS_BASELINE}"
      --max-error-rate-delta-vs-baseline "${TOOL_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE}"
      --out "${parallel_out}"
    )

    (
      cd "${ROOT_DIR}"
      printf '%s\n' "${parallel_cmd[*]}" >> "${OUT_DIR}/commands.txt"
      "${parallel_cmd[@]}" | tee "${parallel_stdout}"
    )
  fi
fi

if [[ "${RUN_IMPORTERS_FIXTURE_PROFILE}" == "1" ]]; then
  profile_id="tool-fixture-importers"
  serial_out="${OUT_DIR}/serial-baseline-${profile_id}.json"
  serial_stdout="${OUT_DIR}/serial-baseline-${profile_id}.stdout.json"

  serial_cmd=(
    go run ./src/cmd/gocodemunch-slo-bench
    --mode serial
    --workload tool_find_importers_fixture
    --fixture-path "${IMPORTERS_FIXTURE_PATH}"
    --min-fixture-file-count "${IMPORTERS_FIXTURE_MIN_FILE_COUNT}"
    --runs "${IMPORTERS_FIXTURE_RUNS}"
    --batch-items "${IMPORTERS_FIXTURE_BATCH_ITEMS}"
    --max-workers 1
    --queue-depth "${QUEUE_DEPTH}"
    --max-error-rate "${MAX_ERROR_RATE}"
    --max-p95-ms "${MAX_P95_MS}"
    --min-throughput-items-per-sec "${IMPORTERS_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
    --out "${serial_out}"
  )

  (
    cd "${ROOT_DIR}"
    printf '%s\n' "${serial_cmd[*]}" >> "${OUT_DIR}/commands.txt"
    "${serial_cmd[@]}" | tee "${serial_stdout}"
  )

  if [[ "${RUN_PARALLEL_SHADOW:-1}" == "1" ]]; then
    parallel_out="${OUT_DIR}/parallel-shadow-${profile_id}.json"
    parallel_stdout="${OUT_DIR}/parallel-shadow-${profile_id}.stdout.json"

    parallel_cmd=(
      go run ./src/cmd/gocodemunch-slo-bench
      --mode parallel
      --workload tool_find_importers_fixture
      --fixture-path "${IMPORTERS_FIXTURE_PATH}"
      --min-fixture-file-count "${IMPORTERS_FIXTURE_MIN_FILE_COUNT}"
      --runs "${IMPORTERS_FIXTURE_RUNS}"
      --batch-items "${IMPORTERS_FIXTURE_BATCH_ITEMS}"
      --max-workers "${PARALLEL_WORKERS}"
      --queue-depth "${QUEUE_DEPTH}"
      --max-error-rate "${MAX_ERROR_RATE}"
      --max-p95-ms "${MAX_P95_MS}"
      --min-throughput-items-per-sec "${IMPORTERS_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
      --baseline-report "${serial_out}"
      --min-throughput-ratio-vs-baseline "${IMPORTERS_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE}"
      --max-p95-ratio-vs-baseline "${IMPORTERS_FIXTURE_MAX_P95_RATIO_VS_BASELINE}"
      --max-error-rate-delta-vs-baseline "${IMPORTERS_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE}"
      --out "${parallel_out}"
    )

    (
      cd "${ROOT_DIR}"
      printf '%s\n' "${parallel_cmd[*]}" >> "${OUT_DIR}/commands.txt"
      "${parallel_cmd[@]}" | tee "${parallel_stdout}"
    )
  fi
fi

if [[ "${RUN_REFERENCES_FIXTURE_PROFILE}" == "1" ]]; then
  profile_id="tool-fixture-find-references"
  serial_out="${OUT_DIR}/serial-baseline-${profile_id}.json"
  serial_stdout="${OUT_DIR}/serial-baseline-${profile_id}.stdout.json"

  serial_cmd=(
    go run ./src/cmd/gocodemunch-slo-bench
    --mode serial
    --workload tool_find_references_fixture
    --fixture-path "${REFERENCES_FIXTURE_PATH}"
    --min-fixture-file-count "${REFERENCES_FIXTURE_MIN_FILE_COUNT}"
    --runs "${REFERENCES_FIXTURE_RUNS}"
    --batch-items "${REFERENCES_FIXTURE_BATCH_ITEMS}"
    --max-workers 1
    --queue-depth "${QUEUE_DEPTH}"
    --max-error-rate "${MAX_ERROR_RATE}"
    --max-p95-ms "${MAX_P95_MS}"
    --min-throughput-items-per-sec "${REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
    --out "${serial_out}"
  )

  (
    cd "${ROOT_DIR}"
    printf '%s\n' "${serial_cmd[*]}" >> "${OUT_DIR}/commands.txt"
    "${serial_cmd[@]}" | tee "${serial_stdout}"
  )

  if [[ "${RUN_PARALLEL_SHADOW:-1}" == "1" ]]; then
    parallel_out="${OUT_DIR}/parallel-shadow-${profile_id}.json"
    parallel_stdout="${OUT_DIR}/parallel-shadow-${profile_id}.stdout.json"

    parallel_cmd=(
      go run ./src/cmd/gocodemunch-slo-bench
      --mode parallel
      --workload tool_find_references_fixture
      --fixture-path "${REFERENCES_FIXTURE_PATH}"
      --min-fixture-file-count "${REFERENCES_FIXTURE_MIN_FILE_COUNT}"
      --runs "${REFERENCES_FIXTURE_RUNS}"
      --batch-items "${REFERENCES_FIXTURE_BATCH_ITEMS}"
      --max-workers "${PARALLEL_WORKERS}"
      --queue-depth "${QUEUE_DEPTH}"
      --max-error-rate "${MAX_ERROR_RATE}"
      --max-p95-ms "${MAX_P95_MS}"
      --min-throughput-items-per-sec "${REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
      --baseline-report "${serial_out}"
      --min-throughput-ratio-vs-baseline "${REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE}"
      --max-p95-ratio-vs-baseline "${REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE}"
      --max-error-rate-delta-vs-baseline "${REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE}"
      --out "${parallel_out}"
    )

    (
      cd "${ROOT_DIR}"
      printf '%s\n' "${parallel_cmd[*]}" >> "${OUT_DIR}/commands.txt"
      "${parallel_cmd[@]}" | tee "${parallel_stdout}"
    )
  fi
fi

if [[ "${RUN_CHECK_REFERENCES_FIXTURE_PROFILE}" == "1" ]]; then
  profile_id="tool-fixture-check-references-content"
  serial_out="${OUT_DIR}/serial-baseline-${profile_id}.json"
  serial_stdout="${OUT_DIR}/serial-baseline-${profile_id}.stdout.json"

  serial_cmd=(
    go run ./src/cmd/gocodemunch-slo-bench
    --mode serial
    --workload tool_check_references_fixture
    --fixture-path "${CHECK_REFERENCES_FIXTURE_PATH}"
    --min-fixture-file-count "${CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT}"
    --runs "${CHECK_REFERENCES_FIXTURE_RUNS}"
    --batch-items "${CHECK_REFERENCES_FIXTURE_BATCH_ITEMS}"
    --max-workers 1
    --queue-depth "${QUEUE_DEPTH}"
    --max-error-rate "${MAX_ERROR_RATE}"
    --max-p95-ms "${MAX_P95_MS}"
    --min-throughput-items-per-sec "${CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
    --out "${serial_out}"
  )

  (
    cd "${ROOT_DIR}"
    printf '%s\n' "${serial_cmd[*]}" >> "${OUT_DIR}/commands.txt"
    "${serial_cmd[@]}" | tee "${serial_stdout}"
  )

  if [[ "${RUN_PARALLEL_SHADOW:-1}" == "1" ]]; then
    parallel_out="${OUT_DIR}/parallel-shadow-${profile_id}.json"
    parallel_stdout="${OUT_DIR}/parallel-shadow-${profile_id}.stdout.json"

    parallel_cmd=(
      go run ./src/cmd/gocodemunch-slo-bench
      --mode parallel
      --workload tool_check_references_fixture
      --fixture-path "${CHECK_REFERENCES_FIXTURE_PATH}"
      --min-fixture-file-count "${CHECK_REFERENCES_FIXTURE_MIN_FILE_COUNT}"
      --runs "${CHECK_REFERENCES_FIXTURE_RUNS}"
      --batch-items "${CHECK_REFERENCES_FIXTURE_BATCH_ITEMS}"
      --max-workers "${PARALLEL_WORKERS}"
      --queue-depth "${QUEUE_DEPTH}"
      --max-error-rate "${MAX_ERROR_RATE}"
      --max-p95-ms "${MAX_P95_MS}"
      --min-throughput-items-per-sec "${CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_ITEMS_PER_SEC}"
      --baseline-report "${serial_out}"
      --min-throughput-ratio-vs-baseline "${CHECK_REFERENCES_FIXTURE_MIN_THROUGHPUT_RATIO_VS_BASELINE}"
      --max-p95-ratio-vs-baseline "${CHECK_REFERENCES_FIXTURE_MAX_P95_RATIO_VS_BASELINE}"
      --max-error-rate-delta-vs-baseline "${CHECK_REFERENCES_FIXTURE_MAX_ERROR_RATE_DELTA_VS_BASELINE}"
      --out "${parallel_out}"
    )

    (
      cd "${ROOT_DIR}"
      printf '%s\n' "${parallel_cmd[*]}" >> "${OUT_DIR}/commands.txt"
      "${parallel_cmd[@]}" | tee "${parallel_stdout}"
    )
  fi
fi

echo "benchmark_run_id=${RUN_ID}"
echo "artifact_dir=${OUT_DIR}"
