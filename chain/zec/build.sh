#!/bin/bash
set -euo pipefail

clear

echo "--- Starting Build Process ---"

# --- Configuration ---
ROOT_LIB="$(cd ../../lib && pwd)"
DELETE_TARGET="${DELETE_TARGET:-false}"
SKIP_E2E="${SKIP_E2E:-0}"
BUILD_TARGET="${BUILD_TARGET:-all}" # Options: all, go, node

# --- Helper Functions ---
print_usage() {
  echo "Usage: $0 [options]"
  echo "Options:"
  echo "  -t, --target <target>  Build target: all, go, node (default: all)"
  echo "  -d, --delete-target    Delete target directory after build"
  echo "  -h, --help            Show this help message"
  echo ""
  echo "Environment variables:"
  echo "  ZEC_FEATURES          Cargo features."
  echo "                       If unset and building Go, builds 3 variants:"
  echo "                         - prod:      'uniffi'"
  echo "                         - sim:       'uniffi shielded'"
  echo "                         - e2e:       'e2e' (implies shielded, tests only; not copied to root/lib)"
  echo "  SKIP_E2E              If set to 1, skip Docker-based E2E tests and skip building the e2e variant in default Go build"
  echo "                       Examples: 'uniffi shielded' (no E2E), 'uniffi e2e' (no shielded)"
  echo ""
  echo "Examples:"
  echo "  $0                         # Build all targets (uniffi + shielded + e2e)"
  echo "  $0 --target go             # Build Go bindings (default features)"
  echo "  ZEC_FEATURES='uniffi shielded' $0 -t go   # Build shielded Go lib only"
  echo "  $0 --target node           # Build Node.js bindings (NAPI)"
}

# --- Parse Arguments ---
while [[ $# -gt 0 ]]; do
  case $1 in
  -t | --target)
    BUILD_TARGET="$2"
    shift 2
    ;;
  -d | --delete-target)
    DELETE_TARGET="true"
    shift
    ;;
  -h | --help)
    print_usage
    exit 0
    ;;
  *)
    echo "Unknown option: $1"
    print_usage
    exit 1
    ;;
  esac
done

# --- Detect Platform ---
KERNEL_NAME=$(uname)
if [ "$KERNEL_NAME" = "Linux" ]; then
  LIBZEC_FILE="libzec.so"
  NODE_MODULE_EXT="linux-x64-gnu.node"
elif [ "$KERNEL_NAME" = "Darwin" ]; then
  LIBZEC_FILE="libzec.dylib"
  NODE_MODULE_EXT="darwin-x64.node"
else
  echo "ERROR: Unsupported kernel: $KERNEL_NAME"
  exit 1
fi

echo "INFO: Building for platform: $KERNEL_NAME"
echo "INFO: Build target: $BUILD_TARGET"

# Track if ZEC_FEATURES was provided by the user
if [ -z "${ZEC_FEATURES+x}" ]; then
  ZEC_FEATURES_PROVIDED=false
else
  ZEC_FEATURES_PROVIDED=true
fi

# Default cargo features (only used if explicitly provided or for Node path)
ZEC_FEATURES_DEFAULT="uniffi shielded e2e"
ZEC_FEATURES="${ZEC_FEATURES:-$ZEC_FEATURES_DEFAULT}"

# Reject deprecated/unsupported features early
if echo " ${ZEC_FEATURES} " | grep -qw regtest; then
  echo "ERROR: 'regtest' is not a supported ZEC_FEATURE. Use 'e2e' (implies shielded) instead." >&2
  exit 1
fi

# --- Build Go Bindings ---
build_go_variant() {
  local FEATURES="$1"     # e.g., "uniffi", "uniffi shielded", "e2e"
  local COPY_VARIANT="$2" # one of: prod | sim | custom | none
  local ORIG_ZF="$ZEC_FEATURES"
  ZEC_FEATURES="$FEATURES"
  echo ""
  echo "=== Building Go Bindings (features: ${ZEC_FEATURES}) ==="

  # Check for uniffi-bindgen-go
  echo "INFO: Checking for uniffi-bindgen-go..."
  if ! command -v uniffi-bindgen-go &>/dev/null; then
    if [[ -d "$HOME/.cargo/bin" && ":$PATH:" != *":$HOME/.cargo/bin:"* ]]; then
      export PATH="$HOME/.cargo/bin:$PATH"
    fi
    if ! command -v uniffi-bindgen-go &>/dev/null; then
      echo "ERROR: uniffi-bindgen-go command not found." >&2
      echo "       Install with: cargo install uniffi-bindgen-go"
      exit 1
    fi
  fi
  echo "INFO: Found uniffi-bindgen-go."

  # Normalize feature set:
  # - If 'e2e' requested but 'shielded' missing, add 'shielded'
  # - If any of 'shielded' or 'e2e' requested and 'uniffi' missing, add 'uniffi'
  if echo " ${ZEC_FEATURES} " | grep -qw e2e && ! echo " ${ZEC_FEATURES} " | grep -qw shielded; then
    ZEC_FEATURES="${ZEC_FEATURES} shielded"
  fi
  if (echo " ${ZEC_FEATURES} " | grep -qw shielded || echo " ${ZEC_FEATURES} " | grep -qw e2e) &&
    ! echo " ${ZEC_FEATURES} " | grep -qw uniffi; then
    ZEC_FEATURES="uniffi ${ZEC_FEATURES}"
  fi

  # Build Rust library with selected features
  echo "INFO: Building Rust library for Go (features: ${ZEC_FEATURES})..."
  if ! cargo build --release --no-default-features --features "${ZEC_FEATURES}"; then
    echo "ERROR: Rust build failed." >&2
    exit 1
  fi

  if [ ! -f "target/release/${LIBZEC_FILE}" ]; then
    echo "ERROR: Compiled Rust library 'target/release/${LIBZEC_FILE}' not found." >&2
    exit 1
  fi
  echo "INFO: Rust library built successfully."

  # Copy library to lib directory according to variant
  mkdir -p "${ROOT_LIB}"
  case "$COPY_VARIANT" in
  prod)
    VARIANT_DIR="${ROOT_LIB}"
    mkdir -p "${VARIANT_DIR}"
    echo "INFO: Copying production variant to ${VARIANT_DIR}/${LIBZEC_FILE}"
    cp "target/release/${LIBZEC_FILE}" "${VARIANT_DIR}/${LIBZEC_FILE}"
    ;;
  sim)
    VARIANT_DIR="${ROOT_LIB}/sim"
    mkdir -p "${VARIANT_DIR}"
    echo "INFO: Copying simulation variant to ${VARIANT_DIR}/${LIBZEC_FILE}"
    cp "target/release/${LIBZEC_FILE}" "${VARIANT_DIR}/${LIBZEC_FILE}"
    ;;
  custom)
    echo "INFO: Not copying Go library for custom build (use target/release for testing)"
    ;;
  none)
    echo "INFO: Not copying Go library to root/lib for this variant"
    ;;
  esac

  # Generate Go bindings
  mkdir -p go/zec
  echo "INFO: Cleaning previous Go bindings..."
  rm -f go/zec/zec.go go/zec/*.c go/zec/*.h

  echo "INFO: Generating Go bindings..."
  if ! uniffi-bindgen-go "src/interface.udl" --out-dir go; then
    echo "ERROR: uniffi-bindgen-go failed." >&2
    exit 1
  fi

  if [ ! -f go/zec/zec.go ]; then
    echo "ERROR: Go binding generation failed." >&2
    exit 1
  fi
  echo "INFO: Go bindings generated successfully."

  # Run Go tests
  echo "INFO: Running Go tests..."
  # Choose library dir for tests based on variant
  local TEST_LIBDIR
  case "$COPY_VARIANT" in
  prod) TEST_LIBDIR="${ROOT_LIB}" ;;
  sim) TEST_LIBDIR="${ROOT_LIB}/sim" ;;
  *) TEST_LIBDIR="$(pwd)/target/release" ;;
  esac
  # Also include root lib (RET) alongside variant dir
  local RET_LIBDIR="${ROOT_LIB}"
  if [ "$KERNEL_NAME" = "Darwin" ]; then
    export DYLD_LIBRARY_PATH="${TEST_LIBDIR}:${RET_LIBDIR}:${DYLD_LIBRARY_PATH-}"
  else
    export LD_LIBRARY_PATH="${TEST_LIBDIR}:${RET_LIBDIR}:${LD_LIBRARY_PATH-}"
  fi
  # Embed rpath to ensure the built test binary prefers both dirs
  CGO_LDFLAGS="-L${TEST_LIBDIR} -L${RET_LIBDIR} -Wl,-rpath,${TEST_LIBDIR}:${RET_LIBDIR} -lradix_engine_toolkit_uniffi -lzec" go test -v -tags mocknet -count=1 ./go/zec/...

  # Optional E2E integration tests: run only if 'e2e' feature is selected and not skipped
  if echo " ${ZEC_FEATURES} " | grep -qw e2e; then
    if [ "$SKIP_E2E" = "1" ]; then
      echo ""
      echo "INFO: SKIP_E2E=1 set; skipping Go E2E tests"
    else
      echo ""
      echo "INFO: Running Go E2E integration tests..."
      if (cd go && ./e2e_full_test.sh); then
        echo "✅ E2E integration tests passed!"
      else
        echo "❌ E2E integration tests failed!"
        exit 1
      fi
    fi
  else
    echo ""
    echo "INFO: Skipping Go E2E tests (missing 'e2e' feature in ZEC_FEATURES)"
  fi
  # Restore original ZEC_FEATURES context
  ZEC_FEATURES="$ORIG_ZF"
}

build_go() {
  # If user did not provide ZEC_FEATURES, build the 3-variant set
  if [ "$ZEC_FEATURES_PROVIDED" = false ]; then
    echo "INFO: ZEC_FEATURES not provided; building prod, sim, and e2e variants"
    build_go_variant "uniffi" prod
    build_go_variant "uniffi shielded" sim
    if [ "$SKIP_E2E" = "1" ]; then
      echo "INFO: SKIP_E2E=1 set; skipping e2e variant build"
    else
      build_go_variant "e2e" none
    fi
    return
  fi

  # Otherwise, build the single requested feature set and copy to default lib path
  build_go_variant "$ZEC_FEATURES" custom
}

# --- Build Node.js Bindings ---
build_node() {
  echo ""
  echo "=== Building Node.js NAPI Bindings ==="

  # Check for Node.js
  if ! command -v node &>/dev/null; then
    echo "ERROR: Node.js is not installed." >&2
    exit 1
  fi
  echo "INFO: Found Node.js $(node --version)"

  # Build Rust library with NAPI feature
  echo "INFO: Building Rust library for Node.js (cargo build --release --no-default-features --features napi)..."
  if ! cargo build --release --no-default-features --features napi; then
    echo "ERROR: Rust NAPI build failed." >&2
    exit 1
  fi

  if [ ! -f "target/release/${LIBZEC_FILE}" ]; then
    echo "ERROR: Compiled NAPI library 'target/release/${LIBZEC_FILE}' not found." >&2
    exit 1
  fi
  echo "INFO: NAPI library built successfully."

  # Copy library as .node module
  echo "INFO: Copying NAPI module to ts/zec.${NODE_MODULE_EXT}"
  mkdir -p ts
  rm -f "ts/zec.${NODE_MODULE_EXT}"
  cp "target/release/${LIBZEC_FILE}" "ts/zec.${NODE_MODULE_EXT}" || {
    echo "ERROR: Failed to copy NAPI module." >&2
    exit 1
  }

  echo "INFO: Node.js NAPI bindings built successfully."

  # Run Node.js tests if package.json exists
  if [ -f "ts/package.json" ]; then
    echo "INFO: Running Node.js tests..."
    if echo " ${ZEC_FEATURES} " | grep -qw e2e && [ "$SKIP_E2E" != "1" ]; then
      # Start regtest container if not running
      echo "INFO: Ensuring zcash regtest node is running for Node.js tests..."
      if ! docker ps | grep -q "zcash-regtest-e2e"; then
        echo "INFO: Starting zcash regtest container..."
        (cd ts && docker compose up -d)
        echo "INFO: Waiting for regtest RPC to become ready..."
        # Wait for RPC to be responsive and initial blocks to be generated
        ATTEMPTS=0
        until docker exec zcash-regtest-e2e zcash-cli -regtest -rpcuser=mayachain -rpcpassword=password getblockcount >/dev/null 2>&1; do
          sleep 3
          ATTEMPTS=$((ATTEMPTS + 1))
          if [ "$ATTEMPTS" -gt 60 ]; then
            echo "ERROR: zcashd RPC did not become ready in time" >&2
            exit 1
          fi
        done
        echo "INFO: RPC up; waiting for initial funding (blocks >= 160)..."
        # Wait for regtest-setup.sh to mine and fund addresses
        ATTEMPTS=0
        while true; do
          COUNT=$(docker exec zcash-regtest-e2e zcash-cli -regtest -rpcuser=mayachain -rpcpassword=password getblockcount)
          if [ "$COUNT" -ge 160 ]; then
            break
          fi
          sleep 3
          ATTEMPTS=$((ATTEMPTS + 1))
          if [ "$ATTEMPTS" -gt 120 ]; then
            echo "WARN: Blockcount still $COUNT; proceeding anyway"
            break
          fi
        done
      else
        echo "INFO: Regtest container already running"
      fi

      # Always stop the container even if tests fail
      cleanup_node_e2e() {
        echo "INFO: Stopping regtest container..."
        (cd ts && (docker compose down || true))
      }
      trap cleanup_node_e2e EXIT
      (cd ts && npm test -- --runInBand)
      # Successful path still triggers trap; optionally disable:
      trap - EXIT
      cleanup_node_e2e
    else
      echo "INFO: Running Node.js unit tests (skipping regtest E2E)"
      (cd ts && npm test -- --testPathIgnorePatterns=regtest-e2e --runInBand)
    fi
  else
    echo "INFO: No ts/package.json found, skipping Node.js tests."
  fi
}

# --- Main Build Logic ---
case $BUILD_TARGET in
all)
  build_go
  build_node
  ;;
go)
  build_go
  ;;
node)
  build_node
  ;;
*)
  echo "ERROR: Invalid build target: $BUILD_TARGET"
  print_usage
  exit 1
  ;;
esac

# --- Optionally Delete Target Folder ---
if [ "$DELETE_TARGET" = "true" ]; then
  echo ""
  echo "INFO: Deleting Rust target directory..."
  rm -rf target
else
  echo ""
  echo "INFO: Keeping Rust target directory."
fi

echo ""
echo "=== Build Complete ==="
echo "Build artifacts:"
if [[ $BUILD_TARGET == "all" || $BUILD_TARGET == "go" ]]; then
  if [ "$ZEC_FEATURES_PROVIDED" = false ]; then
    echo "  - Go library (prod): ${ROOT_LIB}/${LIBZEC_FILE}"
    echo "  - Go library (sim):  ${ROOT_LIB}/sim/${LIBZEC_FILE}"
  fi
  echo "  - Go bindings: go/zec/zec.go"
fi
if [[ $BUILD_TARGET == "all" || $BUILD_TARGET == "node" ]]; then
  echo "  - Node.js module: ts/zec.${NODE_MODULE_EXT}"
fi

exit 0
