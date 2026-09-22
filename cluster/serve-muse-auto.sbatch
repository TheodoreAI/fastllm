#!/bin/bash
# Serve Muse-Glimmer-30B on one whole-card H100 or H200. The vram80g
# constraint excludes dgxh-1's 40 GB MIG slices and lets Slurm choose the
# first suitable accelerator that becomes available.
#
# Submit:
#   sbatch /exascale/share/estradma/llm/scripts/serve-muse-auto.sbatch
# Override examples:
#   PORT=8010 sbatch --time=06:00:00 /exascale/share/estradma/llm/scripts/serve-muse-auto.sbatch
#   MAXLEN=131072 sbatch /exascale/share/estradma/llm/scripts/serve-muse-auto.sbatch

#SBATCH --job-name=muse-auto
#SBATCH --partition=dgxh
#SBATCH --constraint=vram80g
#SBATCH --gres=gpu:1
#SBATCH --cpus-per-task=8
#SBATCH --mem=64G
#SBATCH --time=12:00:00
#SBATCH --output=/exascale/share/estradma/llm/logs/serve-muse-auto-%j.log
#SBATCH --requeue
#SBATCH --mail-type=BEGIN,END,FAIL,REQUEUE
#SBATCH --mail-user=estradma@oregonstate.edu

set -euo pipefail

BASE=/exascale/share/estradma/llm
MODEL=meta-models/Muse-Glimmer-30B
PORT="${PORT:-8010}"
MAXSEQS="${MAXSEQS:-8}"
GPUUTIL="${GPUUTIL:-0.94}"

export VLLM_API_KEY="${VLLM_API_KEY:-$(cat "${BASE}/.vllm-api-key" 2>/dev/null)}"
export HF_HOME="${BASE}/hf"
export XDG_CACHE_HOME="${BASE}/cache"
export TRITON_CACHE_DIR="${BASE}/cache/triton"
export VLLM_CACHE_ROOT="${BASE}/cache/vllm"
export FLASHINFER_CACHE_DIR="${BASE}/cache/flashinfer"
export FLASHINFER_WORKSPACE_BASE="${BASE}/cache/flashinfer"
export VLLM_ALLOW_LONG_MAX_MODEL_LEN=1
export VLLM_USE_FLASHINFER_SAMPLER=0
mkdir -p \
  "${XDG_CACHE_HOME}" \
  "${TRITON_CACHE_DIR}" \
  "${VLLM_CACHE_ROOT}" \
  "${FLASHINFER_CACHE_DIR}"

source /usr/share/lmod/lmod/init/bash
module purge
module load anaconda/24.3
module load cuda/13.0
# shellcheck disable=SC1091
source activate "${VLLM_ENV:-${BASE}/envs/vllm}"

GPU_NAME=$(nvidia-smi --query-gpu=name --format=csv,noheader | sed -n '1p')
GPU_MIB=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits | sed -n '1p' | tr -d ' ')

# Preserve headroom on an 80 GB H100; use the H200's larger memory when Slurm
# places the job there. An explicit MAXLEN always wins.
if [ -z "${MAXLEN:-}" ]; then
  if [ "${GPU_MIB}" -ge 120000 ]; then
    MAXLEN=131072
  else
    MAXLEN=131072
  fi
fi

NODE=$(hostname -f)
echo "============================================================"
echo " model      : ${MODEL}"
echo " node       : ${NODE}"
echo " accelerator: ${GPU_NAME} (${GPU_MIB} MiB)"
echo " port       : ${PORT}"
echo " context    : ${MAXLEN}"
echo " walltime   : $(squeue -h -j "${SLURM_JOB_ID}" -o %L 2>/dev/null || echo unknown) remaining"
echo "============================================================"
echo "Tunnel from the laptop:"
echo "  ssh -N -L ${PORT}:${NODE}:${PORT} -J flip1.engr.oregonstate.edu submit-a.hpc.engr.oregonstate.edu"
echo "API: http://localhost:${PORT}/v1"
echo "Stop: scancel ${SLURM_JOB_ID}"
echo "============================================================"

LOG="${BASE}/logs/serve-muse-auto-${SLURM_JOB_ID}.log"

# Mail on "actually usable", not on "slurm allocated the job". Those differ by
# the several minutes it takes to load weights, and a BEGIN mail would
# point at a port that is not listening yet. Set MAILTO= (empty) to disable.
MAILTO="${MAILTO:-estradma@oregonstate.edu}"
# Compute nodes do NOT have /bin/mail -- only sendmail (verified 2026-09-10 on
# cn-x-1: /bin/mail, /usr/bin/mail and /usr/bin/mailx are all absent, while
# /usr/sbin/sendmail is present). Try mail(1) first, then fall back to sendmail.
notify() {
  [ -n "${MAILTO}" ] || return 0
  if command -v mail >/dev/null 2>&1; then
    if printf '%s\n' "$2" | mail -s "$1" "${MAILTO}" 2>/dev/null; then
      echo "notify   : mailed via mail(1): $1"; return 0
    fi
  fi
  if [ -x /usr/sbin/sendmail ]; then
    NOTIFY_MSG=$(printf 'To: %s\nSubject: %s\nFrom: %s\n\n%s\n' "${MAILTO}" "$1" "${USER}@oregonstate.edu" "$2")
    if printf '%s\n' "${NOTIFY_MSG}" | /usr/sbin/sendmail -t -i 2>/dev/null; then
      echo "notify   : mailed via sendmail: $1"; return 0
    fi
  fi
  echo "notify   : NO MAILER AVAILABLE, not sent: $1" >&2
  return 0
}

python -m vllm.entrypoints.openai.api_server \
  --model "${MODEL}" \
  --served-model-name "${MODEL}" muse-glimmer muse-glimmer-30b \
  --tensor-parallel-size 1 \
  --max-model-len "${MAXLEN}" \
  --max-num-seqs "${MAXSEQS}" \
  --gpu-memory-utilization "${GPUUTIL}" \
  --enable-auto-tool-choice \
  --tool-call-parser muse_glimmer \
  --reasoning-parser muse_glimmer \
  --port "${PORT}" \
  --host 0.0.0.0 &
VLLM_PID=$!

# Poll the API rather than grepping the log for "Application startup complete":
# a 200 from /v1/models is the thing the client actually depends on.
READY=0
for _ in $(seq 1 240); do          # 40 min ceiling
  kill -0 "${VLLM_PID}" 2>/dev/null || break
  if curl -sf --max-time 5 "http://127.0.0.1:${PORT}/v1/models" >/dev/null 2>&1; then
    READY=1; break
  fi
  sleep 10
done

if [ "${READY}" = "1" ]; then
  ELAPSED=$(squeue -h -j "${SLURM_JOB_ID}" -o %M 2>/dev/null || echo "?")
  notify "vLLM READY: ${MODEL##*/} on ${NODE%%.*}" \
"${MODEL} is serving on ${NODE}, port ${PORT}.
Job ${SLURM_JOB_ID}, 1 GPU (${GPU_NAME}), loaded in ${ELAPSED}.
Time remaining: $(squeue -h -j "${SLURM_JOB_ID}" -o %L 2>/dev/null || echo '?')

Tunnel from the laptop:
    ssh -N -L ${PORT}:${NODE}:${PORT} -J flip1.engr.oregonstate.edu submit-a.hpc.engr.oregonstate.edu
    curl http://localhost:${PORT}/v1/models

Stop early and free the GPUs:
    scancel ${SLURM_JOB_ID}

Log: ${LOG}"
else
  notify "vLLM FAILED to start: ${MODEL##*/} (job ${SLURM_JOB_ID})" \
"The server never answered on port ${PORT}.

Last 40 lines of ${LOG}:

$(tr '\r' '\n' < "${LOG}" 2>/dev/null | grep -v '^$' | tail -40)"
fi

# `wait` returns the server's exit status; without relaxing -e a non-zero exit
# here would skip the failure mail below.
set +e
wait "${VLLM_PID}"
RC=$?
set -e

if [ "${RC}" -ne 0 ] && [ "${READY}" = "1" ]; then
  notify "vLLM DIED after serving: ${MODEL##*/} (job ${SLURM_JOB_ID}, rc=${RC})" \
"The server started but exited with status ${RC}.

Last 40 lines of ${LOG}:

$(tr '\r' '\n' < "${LOG}" 2>/dev/null | grep -v '^$' | tail -40)"
fi
exit "${RC}"
