#!/usr/bin/env bash
#
# run.sh - Helper script for EmailMCP EKS deployment and infrastructure management.
# See README.md for details on the project.
#
# Usage:
#   ./run.sh <command> [args]
#
# Common commands:
#   eks-deploy      Full deployment to EKS
#   undeploy-eks    Remove deployment from EKS
#   infra-deploy    Deploy AWS infrastructure (CDK)
#   build-push      Build and push the Docker image to ECR
#   test            Run go test ./...
#   clean           Remove build artifacts
#   help            Show this help message

set -euo pipefail

APP_DIR="emailmcp"
LAMBDA_DIR="emailmcp-config-api"
INFRA_DIR="infrastructure"
K8S_NAMESPACE="emailmcp"
DOCKER_PLATFORM="${DOCKER_PLATFORM:-linux/amd64}"

# Colors for output (if supported)
if [ -t 1 ]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[1;33m'
  BLUE='\033[0;34m'
  NC='\033[0m' # No Color
else
  RED=''
  GREEN=''
  YELLOW=''
  BLUE=''
  NC=''
fi

log_info()    { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[OK]${NC}   $*"; }
log_warn()    { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }

usage() {
  cat <<EOF
EmailMCP EKS Deployment Helper

Usage: ./run.sh <command> [options]

EKS Deployment Commands:
  eks-deploy      Full deployment to EKS (CDK + Build/Push + K8s)
  undeploy-eks    Remove deployment from EKS
  eks-refresh     Rebuild image and restart EKS deployment
  eks-status      Check status of EKS deployment
  eks-logs        Tail logs from EKS

Infrastructure Commands:
  infra-deploy    Deploy AWS infrastructure (CDK; requires EKS OIDC for IRSA)
  infra-destroy   Teardown AWS infrastructure
  synth           Synthesize CDK infrastructure

Build & Test Commands:
  build-push      Build and push Docker image to ECR
  test            Run all Go tests
  vet             Run go vet
  check           Run test + vet
  package-lambda  Package the Python Config API Lambda
  clean           Remove build artifacts

Utilities:
  help            Show this message

Examples:
  ./run.sh eks-deploy
  ./run.sh infra-deploy
  ./run.sh build-push
  ./run.sh eks-status

IRSA / OIDC (required for EKS → Config API):
  Set EKS_OIDC_PROVIDER_ARN in emailmcp/.env (preferred), or in the gitignored
  file infrastructure/cdk.local.json. Without it, infra-deploy refuses to run
  so the IRSA role is not removed. Escape hatch: ALLOW_NO_IRSA=1
EOF
}

# Ensure we are in the project root
if [[ ! -d "${APP_DIR}" || ! -f "${APP_DIR}/go.mod" ]]; then
  log_error "This script must be run from the EmailMCP project root."
  exit 1
fi

load_env() {
  if [[ -f "${APP_DIR}/.env" ]]; then
    log_info "Loading environment from ${APP_DIR}/.env"
    # Export variables from .env (simple parser, ignores comments and empty lines)
    set -a
    # shellcheck disable=SC1091
    source "${APP_DIR}/.env"
    set +a
  fi
}

get_cdk_output() {
  local key="$1"
  # This assumes the stack name contains InfrastructureStack
  aws cloudformation describe-stacks --query "Stacks[?contains(StackName, 'InfrastructureStack')].Outputs[] | [?OutputKey=='$key'].OutputValue" --output text 2>/dev/null || echo ""
}

run_cdk() {
  local cdk_cmd="$1"
  shift || true
  local args=("$@")

  # Silence jsii warning for Node versions not yet tested by the CDK toolchain (e.g. v26).
  export JSII_SILENCE_WARNING_UNTESTED_NODE_VERSION=1

  if [[ -n "${GOOGLE_CLIENT_ID:-}" ]]; then
    # Check if googleClientId context is already provided to avoid duplicates
    local has_google_id=0
    for arg in ${args[@]+"${args[@]}"}; do
      if [[ "$arg" == *"googleClientId="* ]]; then
        has_google_id=1
        break
      fi
    done
    if [[ $has_google_id -eq 0 ]]; then
      args=("--context" "googleClientId=${GOOGLE_CLIENT_ID}" ${args[@]+"${args[@]}"})
    fi
  fi

  (cd "${INFRA_DIR}" && npx cdk "${cdk_cmd}" ${args[@]+"${args[@]}"})
}

# --- EKS Deployment Commands ---

set_docker_platform() {
  log_info "Detecting EKS node architecture..."
  local node_arch=$(kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}' 2>/dev/null || echo "amd64")
  case "$node_arch" in
    x86_64|amd64) DOCKER_PLATFORM="linux/amd64" ;;
    arm64|aarch64) DOCKER_PLATFORM="linux/arm64" ;;
    *) DOCKER_PLATFORM="linux/amd64" ;;
  esac
  log_info "Targeting platform: $DOCKER_PLATFORM"
  export DOCKER_PLATFORM
}

cmd_eks_deploy() {
  # 1. Detect target architecture
  set_docker_platform

  # 2. Deploy Infrastructure (CDK).
  # cmd_infra_deploy always resolves/requires OIDC context for IRSA.
  log_info "Ensuring infrastructure is up to date..."
  cmd_infra_deploy
  local repo_uri=$(get_cdk_output "EcrRepositoryUri")
  local config_api_url=$(get_cdk_output "ConfigApiUrl")
  local irsa_role_arn=$(get_cdk_output "IrsaRoleArn")
  local cert_arn="${EKS_CERTIFICATE_ARN:-}"
  if [[ -z "$cert_arn" ]]; then
    cert_arn=$(get_cdk_output "CertificateArn")
  fi
  
  if [[ -z "$repo_uri" || "$repo_uri" == "None" || -z "$config_api_url" || "$config_api_url" == "None" ]]; then
     log_error "Missing CDK outputs (EcrRepositoryUri or ConfigApiUrl)."
     log_info "Run ./run.sh infra-deploy first to provision ECR and Config API."
     exit 1
  fi

  # 5. Build and push image
  cmd_build_push

  # 6. Apply manifests to EKS
  cmd_eks_apply
  
  log_success "EKS deployment complete"
}

cmd_eks_apply() {
  log_info "Preparing Kubernetes manifests..."
  local k8s_dir="${APP_DIR}/deploy/eks"
  
  local repo_uri=$(get_cdk_output "EcrRepositoryUri")
  local config_api_url=$(get_cdk_output "ConfigApiUrl")
  local irsa_role_arn=$(get_cdk_output "IrsaRoleArn")
  local cert_arn="${EKS_CERTIFICATE_ARN:-}"
  if [[ -z "$cert_arn" ]]; then
    cert_arn=$(get_cdk_output "CertificateArn")
  fi
  
  if [[ -z "$repo_uri" || "$repo_uri" == "None" || -z "$config_api_url" || "$config_api_url" == "None" ]]; then
     log_error "Missing CDK outputs (EcrRepositoryUri or ConfigApiUrl)."
     log_info "Run ./run.sh infra-deploy first to provision ECR and Config API."
     exit 1
  fi

  export ECR_REPO_URL="$repo_uri"
  export CONFIG_API_URL="$config_api_url"
  export IRSA_ROLE_ARN="$irsa_role_arn"
  export AWS_REGION="${AWS_REGION:-$(aws configure get region || echo "us-east-1")}"
  export GOOGLE_CLIENT_ID="${GOOGLE_CLIENT_ID:-}"
  export GOOGLE_CLIENT_SECRET="${GOOGLE_CLIENT_SECRET:-}"
  export PUBLIC_BASE_URL="${PUBLIC_BASE_URL:-https://emailmcp.ecg.co}"
  export APPLICATION_ID="${APPLICATION_ID:-default}"
  export EMAILMCP_LOG_LEVEL="${EMAILMCP_LOG_LEVEL:-info}"
  export CERTIFICATE_ARN="$cert_arn"

  if [[ -z "${GOOGLE_CLIENT_ID}" ]]; then
    log_error "GOOGLE_CLIENT_ID is required for EKS deployment."
    log_info "Set it in emailmcp/.env or export GOOGLE_CLIENT_ID=..."
    exit 1
  fi
  if [[ -z "${GOOGLE_CLIENT_SECRET}" ]]; then
    log_error "GOOGLE_CLIENT_SECRET is required for EKS deployment (MCP OAuth via Google)."
    log_info "Set it in emailmcp/.env or export GOOGLE_CLIENT_SECRET=..."
    exit 1
  fi

  # Apply to cluster
  log_info "Applying manifests to EKS..."
  
  kubectl apply -f "${k8s_dir}/namespace.yaml"
  
  if command -v envsubst >/dev/null 2>&1; then
    envsubst < "${k8s_dir}/serviceaccount.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
    envsubst < "${k8s_dir}/configmap.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
    envsubst < "${k8s_dir}/secret.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
    envsubst < "${k8s_dir}/deployment.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
    envsubst < "${k8s_dir}/ingress.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
  else
    log_warn "envsubst not found. Using sed (limited support)."
    
    sed -e "s|\${IRSA_ROLE_ARN}|${IRSA_ROLE_ARN}|g" \
        "${k8s_dir}/serviceaccount.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
  
    sed -e "s|\${CONFIG_API_URL}|${CONFIG_API_URL}|g" \
        -e "s|\${AWS_REGION}|${AWS_REGION}|g" \
        -e "s|\${GOOGLE_CLIENT_ID}|${GOOGLE_CLIENT_ID}|g" \
        -e "s|\${APPLICATION_ID}|${APPLICATION_ID}|g" \
        -e "s|\${EMAILMCP_LOG_LEVEL}|${EMAILMCP_LOG_LEVEL}|g" \
        -e "s|\${PUBLIC_BASE_URL}|${PUBLIC_BASE_URL}|g" \
        "${k8s_dir}/configmap.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -

    sed -e "s|\${GOOGLE_CLIENT_SECRET}|${GOOGLE_CLIENT_SECRET}|g" \
        "${k8s_dir}/secret.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
        
    sed -e "s|\${ECR_REPO_URL}|${ECR_REPO_URL}|g" \
        "${k8s_dir}/deployment.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -

    sed -e "s|\${CERTIFICATE_ARN}|${CERTIFICATE_ARN}|g" \
        "${k8s_dir}/ingress.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
  fi
}

cmd_eks_status() {
  log_info "Checking EKS deployment status in namespace ${K8S_NAMESPACE}..."
  kubectl get pods,svc,deploy,ing -n "${K8S_NAMESPACE}" -l app=emailmcp
}

cmd_eks_logs() {
  log_info "Tailing EKS logs in namespace ${K8S_NAMESPACE} (Ctrl+C to stop)..."
  kubectl logs -n "${K8S_NAMESPACE}" -l app=emailmcp --tail=100 -f
}

cmd_eks_refresh() {
  log_info "Refreshing EKS deployment..."
  
  # 1. Detect target architecture
  set_docker_platform

  # 2. Build and push image
  cmd_build_push

  # 3. Re-apply manifests (syncs ConfigMap/Secret/Deployment)
  cmd_eks_apply

  # 4. Trigger rollout restart to ensure the new image is pulled
  log_info "Triggering rollout restart of deployment/emailmcp..."
  kubectl rollout restart deployment/emailmcp -n "${K8S_NAMESPACE}"
  kubectl rollout status deployment/emailmcp -n "${K8S_NAMESPACE}"
  
  log_success "EKS deployment refreshed"
}

cmd_eks_undeploy() {
  log_info "Removing EmailMCP resources from EKS namespace ${K8S_NAMESPACE}..."
  
  kubectl delete deployment emailmcp -n "${K8S_NAMESPACE}" --ignore-not-found
  kubectl delete service emailmcp -n "${K8S_NAMESPACE}" --ignore-not-found
  kubectl delete ingress emailmcp -n "${K8S_NAMESPACE}" --ignore-not-found
  kubectl delete secret emailmcp-secrets -n "${K8S_NAMESPACE}" --ignore-not-found
  kubectl delete configmap emailmcp-config -n "${K8S_NAMESPACE}" --ignore-not-found
  kubectl delete serviceaccount emailmcp -n "${K8S_NAMESPACE}" --ignore-not-found
  
  log_success "EKS undeploy complete"
}

# Resolve EKS cluster name from kubeconfig / env.
# Handles forms like "arn:aws:eks:...:cluster/name", "name.us-east-1.eksctl.io", or bare names.
detect_eks_cluster_name() {
  if [[ -n "${EKS_CLUSTER_NAME:-}" ]]; then
    echo "$EKS_CLUSTER_NAME"
    return 0
  fi

  local raw
  raw=$(kubectl config view --minify -o jsonpath='{.clusters[0].name}' 2>/dev/null || echo "")
  if [[ -z "$raw" ]]; then
    echo ""
    return 0
  fi

  # arn:aws:eks:region:account:cluster/name
  if [[ "$raw" == *":cluster/"* ]]; then
    echo "${raw##*:cluster/}"
    return 0
  fi

  # eksctl style: <name>.<region>.eksctl.io
  if [[ "$raw" == *.eksctl.io ]]; then
    echo "${raw%%.*}"
    return 0
  fi

  echo "$raw"
}

# True if argv already includes eksOidcProvider / eksOidcProviderArn context.
cdk_args_have_oidc() {
  local arg
  for arg in "$@"; do
    if [[ "$arg" == *eksOidcProvider* ]]; then
      return 0
    fi
  done
  return 1
}

# Local-only OIDC config (gitignored). Never write account/cluster IDs to cdk.json.
CDK_LOCAL_JSON="${INFRA_DIR}/cdk.local.json"

# Read OIDC settings from infrastructure/cdk.local.json (if any).
read_oidc_from_cdk_local() {
  if [[ ! -f "$CDK_LOCAL_JSON" ]]; then
    return 0
  fi
  # Prefer ARN; fall back to issuer URL/name.
  python3 - "$CDK_LOCAL_JSON" <<'PY' 2>/dev/null || true
import json, sys
path = sys.argv[1]
with open(path) as f:
    data = json.load(f)
# Support either flat keys or a "context" object (CDK-style).
ctx = data.get("context") if isinstance(data.get("context"), dict) else data
arn = (ctx or {}).get("eksOidcProviderArn") or ""
issuer = (ctx or {}).get("eksOidcProvider") or ""
if arn:
    print(f"arn={arn}")
elif issuer:
    print(f"issuer={issuer}")
PY
}

# Persist discovered OIDC into gitignored infrastructure/cdk.local.json so
# future deploys keep IRSA without re-detecting or committing env-specific IDs.
persist_oidc_to_cdk_local() {
  local arn="${1:-}"
  local issuer="${2:-}"
  if [[ -z "$arn" && -z "$issuer" ]]; then
    return 0
  fi
  python3 - "$CDK_LOCAL_JSON" "$arn" "$issuer" <<'PY'
import json, sys, os
path, arn, issuer = sys.argv[1], sys.argv[2], sys.argv[3]
data = {}
if os.path.exists(path):
    with open(path) as f:
        data = json.load(f)
changed = False
if arn and data.get("eksOidcProviderArn") != arn:
    data["eksOidcProviderArn"] = arn
    changed = True
if issuer and data.get("eksOidcProvider") != issuer:
    data["eksOidcProvider"] = issuer
    changed = True
if changed or not os.path.exists(path):
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")
    print("updated")
else:
    print("unchanged")
PY
}

# Append two elements to a caller-owned array by name (bash 3.2 compatible;
# namerefs / local -n require bash 4.3+ and fail on macOS /bin/bash).
# Usage: array_append_2 cdk_args "--context" "eksOidcProviderArn=..."
array_append_2() {
  local _array_name=$1
  local _a=$2
  local _b=$3
  eval "${_array_name}+=(\"\${_a}\" \"\${_b}\")"
}

# Populate a named cdk_args array with OIDC context for IRSA.
# Resolution order:
#   1. EKS_OIDC_PROVIDER_ARN / EKS_OIDC_PROVIDER env (from .env or shell)
#   2. infrastructure/cdk.local.json (durable, gitignored)
#   3. Live detect from current kubectl cluster (EKS_CLUSTER_NAME or kubeconfig)
# Returns 0 if OIDC context was added or already present; 1 if unresolved.
# Usage: resolve_eks_oidc_cdk_args cdk_args   # pass array *name*
resolve_eks_oidc_cdk_args() {
  local array_name=$1
  local oidc_provider="${EKS_OIDC_PROVIDER:-}"
  local oidc_provider_arn="${EKS_OIDC_PROVIDER_ARN:-}"

  if [[ -n "$oidc_provider_arn" ]]; then
    log_info "Using OIDC Provider ARN from environment: $oidc_provider_arn"
    array_append_2 "$array_name" "--context" "eksOidcProviderArn=${oidc_provider_arn}"
    return 0
  fi

  if [[ -n "$oidc_provider" ]]; then
    log_info "Using OIDC Provider from environment: $oidc_provider"
    array_append_2 "$array_name" "--context" "eksOidcProvider=${oidc_provider}"
    return 0
  fi

  # Local durable fallback (gitignored — not committed)
  local from_local
  from_local=$(read_oidc_from_cdk_local)
  if [[ "$from_local" == arn=* ]]; then
    oidc_provider_arn="${from_local#arn=}"
    log_info "Using OIDC Provider ARN from ${CDK_LOCAL_JSON}: $oidc_provider_arn"
    array_append_2 "$array_name" "--context" "eksOidcProviderArn=${oidc_provider_arn}"
    export EKS_OIDC_PROVIDER_ARN="$oidc_provider_arn"
    return 0
  fi
  if [[ "$from_local" == issuer=* ]]; then
    oidc_provider="${from_local#issuer=}"
    log_info "Using OIDC Provider from ${CDK_LOCAL_JSON}: $oidc_provider"
    array_append_2 "$array_name" "--context" "eksOidcProvider=${oidc_provider}"
    export EKS_OIDC_PROVIDER="$oidc_provider"
    return 0
  fi

  # Live detection from the active cluster
  local cluster_name
  cluster_name=$(detect_eks_cluster_name)
  if [[ -n "$cluster_name" ]]; then
    log_info "Detected cluster name: $cluster_name"
    oidc_provider=$(aws eks describe-cluster --name "$cluster_name" --query "cluster.identity.oidc.issuer" --output text 2>/dev/null || echo "")
    if [[ "$oidc_provider" == "None" ]]; then
      oidc_provider=""
    fi
  fi

  if [[ -n "$oidc_provider" ]]; then
    log_info "Using OIDC Provider from cluster: $oidc_provider"
    array_append_2 "$array_name" "--context" "eksOidcProvider=${oidc_provider}"
    export EKS_OIDC_PROVIDER="$oidc_provider"
    # Persist locally (gitignored) so the next deploy works without kubectl
    local persist_result
    persist_result=$(persist_oidc_to_cdk_local "" "$oidc_provider" || true)
    if [[ "$persist_result" == "updated" ]]; then
      log_success "Saved OIDC provider to ${CDK_LOCAL_JSON} (gitignored) for future deploys"
    fi
    return 0
  fi

  return 1
}

# Ensure OIDC context is on the named array, or abort.
# IRSA is conditional in the CDK stack: deploying without OIDC *deletes* the
# IRSA role. Fail closed unless ALLOW_NO_IRSA=1.
# Usage: ensure_eks_oidc_for_deploy cdk_args   # pass array *name*
ensure_eks_oidc_for_deploy() {
  local array_name=$1
  # Copy named array elements for inspection (bash 3.2 — no namerefs).
  # Under set -u, expanding an empty ${arr[@]} is an unbound-variable error on
  # bash 3.2 (macOS /bin/bash); only expand when the array has elements.
  local _existing_args=()
  if eval "[[ \${#${array_name}[@]} -gt 0 ]]"; then
    eval "_existing_args=(\"\${${array_name}[@]}\")"
  fi

  if cdk_args_have_oidc ${_existing_args[@]+"${_existing_args[@]}"}; then
    return 0
  fi

  if resolve_eks_oidc_cdk_args "$array_name"; then
    return 0
  fi

  local existing_irsa
  existing_irsa=$(get_cdk_output "IrsaRoleArn")
  if [[ -n "$existing_irsa" && "$existing_irsa" != "None" ]]; then
    log_error "Refusing to deploy: stack already has IRSA (${existing_irsa})"
    log_error "but no EKS OIDC context was found. Deploying now would DELETE the IRSA role."
  else
    log_error "Could not resolve EKS OIDC provider for IRSA."
  fi

  log_info "Fix by doing one of the following (preferred first):"
  log_info "  1. Put in ${APP_DIR}/.env (gitignored):"
  log_info "       EKS_OIDC_PROVIDER_ARN=arn:aws:iam::ACCOUNT:oidc-provider/oidc.eks.REGION.amazonaws.com/id/XXXX"
  log_info "  2. Or put in ${CDK_LOCAL_JSON} (gitignored):"
  log_info "       {\"eksOidcProviderArn\": \"arn:aws:iam::ACCOUNT:oidc-provider/...\"}"
  log_info "  3. Or export EKS_CLUSTER_NAME=<cluster> with kubectl configured, then re-run"
  log_info "  4. Or pass: --context eksOidcProviderArn=arn:aws:iam::..."
  log_info "Do not commit OIDC ARNs to cdk.json / git — they identify your account and cluster."
  log_info "To deploy WITHOUT IRSA on purpose: ALLOW_NO_IRSA=1 ./run.sh infra-deploy"

  if [[ "${ALLOW_NO_IRSA:-}" == "1" ]]; then
    log_warn "ALLOW_NO_IRSA=1 set; continuing without IRSA (role will be removed if present)."
    return 0
  fi
  exit 1
}

# --- Infrastructure Commands ---

cmd_infra_deploy() {
  log_info "Starting cloud deployment..."
  cmd_package_lambda

  # Always attach IRSA OIDC context. Missing context synthesizes a stack
  # without the IRSA role and CloudFormation deletes it.
  local cdk_args=("$@")
  ensure_eks_oidc_for_deploy cdk_args

  log_info "Running cdk deploy..."
  run_cdk deploy ${cdk_args[@]+"${cdk_args[@]}"}
}

cmd_infra_destroy() {
  log_info "Tearing down infrastructure..."
  run_cdk destroy "$@"
}

cmd_synth() {
  cmd_package_lambda
  local cdk_args=("$@")
  # Synth should match deploy; fail closed the same way.
  ensure_eks_oidc_for_deploy cdk_args
  log_info "Running cdk synth..."
  run_cdk synth ${cdk_args[@]+"${cdk_args[@]}"}
}

cmd_package_lambda() {
  log_info "Packaging Python Lambda function..."
  # Clean old dist
  rm -rf "${LAMBDA_DIR}/dist"
  mkdir -p "${LAMBDA_DIR}/dist"
  
  # Copy main.py
  cp "${LAMBDA_DIR}/main.py" "${LAMBDA_DIR}/dist/"
  
  # Install Linux x86_64 deps for the Lambda runtime. Host pip (e.g. macOS arm64)
  # produces Darwin binaries that fail on Lambda with "invalid ELF header".
  local lambda_python_version="3.14"
  local lambda_platform="manylinux2014_x86_64"

  log_info "  - Installing Python dependencies for Lambda (${lambda_platform}, cp${lambda_python_version//./})..."

  # Always target manylinux wheels. Host pip on macOS otherwise installs Darwin
  # .so files that fail on Lambda with "invalid ELF header".
  # --platform requires --only-binary=:all: (no sdist builds for foreign targets).
  if ! python3 -m pip install \
      -r "${LAMBDA_DIR}/requirements.txt" \
      -t "${LAMBDA_DIR}/dist" \
      --platform "${lambda_platform}" \
      --implementation cp \
      --python-version "${lambda_python_version}" \
      --only-binary=:all: \
      --upgrade \
      --quiet; then
    log_error "Failed to install manylinux wheels for Lambda."
    log_info "If wheels are missing for python ${lambda_python_version}, try packaging via Docker:"
    log_info "  docker run --rm --platform linux/amd64 -v \"\$(pwd)/${LAMBDA_DIR}:/var/task\" -w /var/task public.ecr.aws/lambda/python:${lambda_python_version} pip install -r requirements.txt -t dist"
    exit 1
  fi

  # Ensure main.py is present after dependency install
  cp "${LAMBDA_DIR}/main.py" "${LAMBDA_DIR}/dist/"

  # Fail fast if a macOS-native extension slipped into the package
  if find "${LAMBDA_DIR}/dist" \( -name '*darwin*' -o -name '*.dylib' \) 2>/dev/null | grep -q .; then
    log_error "Lambda package contains macOS-native binaries."
    find "${LAMBDA_DIR}/dist" \( -name '*darwin*' -o -name '*.dylib' \) 2>/dev/null || true
    exit 1
  fi

  if ! find "${LAMBDA_DIR}/dist" -name '*.so' 2>/dev/null | grep -q .; then
    log_warn "No .so extensions found in Lambda package (ok if pure-Python deps only)."
  else
    log_info "  - Sample native extension: $(find "${LAMBDA_DIR}/dist" -name '*.so' | head -1 | xargs file)"
  fi
  
  log_success "Lambda packaged in ${LAMBDA_DIR}/dist"
}

# --- Build & Push Commands ---

cmd_build_push() {
  log_info "Building and pushing Docker image..."
  cmd_docker_build
  cmd_docker_push
}

cmd_docker_build() {
  log_info "Building Docker image 'emailmcp' for platform ${DOCKER_PLATFORM}..."
  docker build --platform "${DOCKER_PLATFORM}" -t emailmcp "${APP_DIR}"
  log_success "Docker image 'emailmcp' built"
}

cmd_docker_push() {
  local repo_uri=$(get_cdk_output "EcrRepositoryUri")
  if [[ -z "$repo_uri" || "$repo_uri" == "None" ]]; then
    log_error "Could not find EcrRepositoryUri in CDK outputs."
    log_info "Make sure you have deployed the infrastructure first: ./run.sh infra-deploy"
    exit 1
  fi

  log_info "Logging into ECR..."
  local region=$(echo "$repo_uri" | cut -d. -f4)
  aws ecr get-login-password --region "$region" | docker login --username AWS --password-stdin "$repo_uri"

  log_info "Tagging and pushing image to ${repo_uri}:latest..."
  docker tag emailmcp:latest "${repo_uri}:latest"
  docker push "${repo_uri}:latest"
  log_success "Image pushed to ECR"
}

# --- Quality & Utilities ---

cmd_test() {
  log_info "Running tests..."
  (cd "${APP_DIR}" && go test ./...)
  log_success "Tests passed"
}

cmd_vet() {
  log_info "Running go vet..."
  (cd "${APP_DIR}" && go vet ./...)
  log_success "Vet passed"
}

cmd_check() {
  cmd_test
  cmd_vet
  log_success "All checks passed"
}

cmd_clean() {
  log_info "Cleaning build artifacts..."

  # Remove common temp files
  rm -f *.out 2>/dev/null || true
  rm -rf "${LAMBDA_DIR}/dist" 2>/dev/null || true
  rm -rf "${INFRA_DIR}/cdk.out" 2>/dev/null || true

  log_success "Clean complete"
}

main() {
  load_env
  if [[ $# -eq 0 ]]; then
    usage
    exit 0
  fi

  local cmd="$1"
  shift || true

  case "$cmd" in
    eks-deploy|eks_deploy)
      cmd_eks_deploy "$@"
      ;;
    eks-undeploy|eks_undeploy|undeploy-eks)
      cmd_eks_undeploy "$@"
      ;;
    eks-status|eks_status)
      cmd_eks_status "$@"
      ;;
    eks-logs|eks_logs)
      cmd_eks_logs "$@"
      ;;
    eks-refresh|eks_refresh|refresh-eks)
      cmd_eks_refresh "$@"
      ;;
    infra-deploy|infra_deploy)
      cmd_infra_deploy "$@"
      ;;
    infra-destroy|infra_destroy)
      cmd_infra_destroy "$@"
      ;;
    synth)
      cmd_synth "$@"
      ;;
    build-push|build_push)
      cmd_build_push "$@"
      ;;
    test)
      cmd_test "$@"
      ;;
    vet)
      cmd_vet "$@"
      ;;
    check)
      cmd_check "$@"
      ;;
    package-lambda|package_lambda)
      cmd_package_lambda "$@"
      ;;
    clean)
      cmd_clean "$@"
      ;;
    help|-h|--help)
      usage
      ;;
    # Backward compatibility for common subcommands
    deploy)
      sub="${1:-}"
      shift || true
      case "$sub" in
        cloud) cmd_infra_deploy "$@" ;;
        docker) cmd_build_push "$@" ;;
        *) usage; exit 1 ;;
      esac
      ;;
    destroy)
      cmd_infra_destroy "$@"
      ;;
    *)
      log_error "Unknown command: $cmd"
      echo
      usage
      exit 1
      ;;
  esac
}

main "$@"
