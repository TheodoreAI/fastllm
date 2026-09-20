#!/bin/bash
# Serve Muse-Glimmer-30B on one whole-card H100 or H200. The vram80g
# constraint excludes dgxh-1's 40 GB MIG slices and lets Slurm choose the
# first suitable accelerator that becomes available.
#
# Submit:
#   sbatch /exascale/share/estradma/llm/scripts/serve-muse-auto.sbatch
# Override examples:
#   PORT=8010 sbatch --time=06:00:00 /exascale/share/estradma/llm/scripts/serve-muse-auto.sbatch
#   MAXLEN=65536 sbatch /exascale/share/estradma/llm/scripts/serve-muse-auto.sbatch

#SBATCH --job-name=muse-auto
#SBATCH --partition=dgxh
#SBATCH --constraint=vram80g
#SBATCH --gres=gpu:1
#SBATCH --cpus-per-task=8
#SBATCH --mem=64G
#SBATCH --time=12:00:00
#SBATCH --output=/exascale/share/estradma/llm/logs/serve-muse-auto-%j.log
#SBATCH --requeue
#SBATCH --mail-type=END,FAIL,REQUEUE
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
    MAXLEN=32768
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

exec python -m vllm.entrypoints.openai.api_server \
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
  --host 0.0.0.0
