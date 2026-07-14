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
#   key             Generate a new 32-byte base64 master key
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
  infra-deploy    Deploy AWS infrastructure (CDK)
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
  key             Generate a new master encryption key
  help            Show this message

Examples:
  ./run.sh eks-deploy
  ./run.sh infra-deploy
  ./run.sh build-push
  ./run.sh eks-status
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

require_master_key() {
  if [[ -z "${EMAILMCP_MASTER_KEY:-}" ]]; then
    log_error "EMAILMCP_MASTER_KEY is not set."
    log_info "Generate one with: ./run.sh key"
    log_info "Or export it: export EMAILMCP_MASTER_KEY=..."
    log_info "Or put it in a .env file for use with deployment commands."
    exit 1
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
  require_master_key
  
  # 1. Detect target architecture
  set_docker_platform

  # 2. Determine EKS OIDC Provider for IRSA
  log_info "Determining EKS OIDC Provider..."
  local oidc_provider="${EKS_OIDC_PROVIDER:-}"
  local oidc_provider_arn="${EKS_OIDC_PROVIDER_ARN:-}"
  
  # 3. Prepare CDK arguments
  local cdk_args=()
  
  if [[ -n "$oidc_provider_arn" ]]; then
    log_info "Using OIDC Provider ARN: $oidc_provider_arn"
    cdk_args+=("--context" "eksOidcProviderArn=${oidc_provider_arn}")
  else
    if [[ -z "$oidc_provider" ]]; then
      local cluster_name=$(kubectl config view --minify -o jsonpath='{.clusters[0].name}' | cut -d/ -f2 || echo "")
      if [[ -n "$cluster_name" ]]; then
        log_info "Detected cluster name: $cluster_name"
        oidc_provider=$(aws eks describe-cluster --name "$cluster_name" --query "cluster.identity.oidc.issuer" --output text 2>/dev/null || echo "")
      fi
    fi

    if [[ -n "$oidc_provider" ]]; then
      log_info "Using OIDC Provider: $oidc_provider"
      cdk_args+=("--context" "eksOidcProvider=${oidc_provider}")
      export EKS_OIDC_PROVIDER="$oidc_provider"
    else
      log_warn "Could not detect EKS OIDC Provider. IRSA will not be configured."
      log_info "You can set EKS_OIDC_PROVIDER or EKS_OIDC_PROVIDER_ARN manually if needed."
    fi
  fi

  # 4. Deploy Infrastructure (CDK)
  log_info "Ensuring infrastructure is up to date..."
  cmd_infra_deploy "${cdk_args[@]}"

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
  require_master_key
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
  export APPLICATION_ID="${APPLICATION_ID:-default}"
  export EMAILMCP_MASTER_KEY_B64=$(echo -n "$EMAILMCP_MASTER_KEY" | base64)
  export CERTIFICATE_ARN="$cert_arn"

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
        "${k8s_dir}/configmap.yaml" | kubectl apply -n "${K8S_NAMESPACE}" -f -
    
    sed -e "s|\${EMAILMCP_MASTER_KEY_B64}|${EMAILMCP_MASTER_KEY_B64}|g" \
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

# --- Infrastructure Commands ---

cmd_infra_deploy() {
  log_info "Starting cloud deployment..."
  cmd_package_lambda
  log_info "Running cdk deploy..."
  run_cdk deploy "$@"
}

cmd_infra_destroy() {
  log_info "Tearing down infrastructure..."
  run_cdk destroy "$@"
}

cmd_synth() {
  cmd_package_lambda
  log_info "Running cdk synth..."
  run_cdk synth "$@"
}

cmd_package_lambda() {
  log_info "Packaging Python Lambda function..."
  # Clean old dist
  rm -rf "${LAMBDA_DIR}/dist"
  mkdir -p "${LAMBDA_DIR}/dist"
  
  # Copy main.py
  cp "${LAMBDA_DIR}/main.py" "${LAMBDA_DIR}/dist/"
  
  # Install dependencies to dist folder
  log_info "  - Installing Python dependencies..."
  python3 -m pip install -r "${LAMBDA_DIR}/requirements.txt" -t "${LAMBDA_DIR}/dist" --quiet
  
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

cmd_key() {
  if ! command -v openssl >/dev/null 2>&1; then
    log_error "openssl is required to generate a key."
    log_info "You can generate one manually with: head -c 32 /dev/urandom | base64"
    exit 1
  fi

  log_info "Generating new 32-byte base64 master key..."
  key=$(openssl rand -base64 32)

  echo
  echo "=========================================="
  echo "EMAILMCP_MASTER_KEY=${key}"
  echo "=========================================="
  echo
  log_success "Copy the value above into your .env file or export it."
  log_warn "Keep this key secret. It is used to encrypt all credentials."
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
    key)
      cmd_key "$@"
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
