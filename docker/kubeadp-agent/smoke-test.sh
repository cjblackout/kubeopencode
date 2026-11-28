#!/usr/bin/env bash
# Smoke test for the kubeadp-agent image, run in CI between the final image
# build and `docker push` (see .github/workflows/build-kubeadp-agent.yml).
# Covers BOTH old images' checks in one superset:
#   ai-server:      claude, node, npm, npx, git, tini, code-server (dropped:
#                   code-server is gone from the image), orchestrator role
#   agent-run-api:  worker role boot on /healthz, agy, git, curl, gcc, make,
#                   pkg-config
#   new:            the role dispatcher (bogus role -> exit 2), agent-run,
#                   opencode (KA-16: bundled engine CLI), ttyd (version +
#                   listen-and-serve)
# Mounted read-only into the container -- deliberately NOT COPY'd into the
# image (an extra COPY/RUN layer would be another full-image copy on the
# runners' vfs storage driver).
#
# Runs as the image's default user (agent), no root, no network. Each command
# is bounded with `timeout 20`. Prints one line per check:
#   ok <name>: <first line of version output>
#   FAIL <name>: <reason>
# Runs ALL checks even after a failure; exits 1 if any failed.

set -u

TCMD=""
if command -v timeout >/dev/null 2>&1; then TCMD="timeout 20"; fi

FAILS=0

fail() { echo "FAIL $1: $2"; FAILS=$((FAILS + 1)); }

# check <name> <command...> -- run the command; report first output line.
check() {
  local name=$1 cmd=$2 tool
  tool=${cmd%% *}
  if ! command -v "$tool" >/dev/null 2>&1; then
    fail "$name" "not in PATH: $tool"
    return
  fi
  local out rc
  if out=$($TCMD $cmd 2>&1); then rc=0; else rc=$?; fi
  if [ "$rc" -eq 124 ] || [ "$rc" -eq 125 ]; then
    fail "$name" "timed out after 20s"
    return
  fi
  if [ "$rc" -eq 0 ]; then
    echo "ok $name: $(printf '%s\n' "$out" | sed -n '1p')"
  else
    fail "$name" "$(printf '%s\n' "$out" | sed -n '1p')"
  fi
}

# check_version <name> [tool] -- <tool> defaults to <name>. Runs
# <tool> --version; if unsupported falls back to <tool> --help and accepts
# exit 0 (the tool exists and launches; only its --version flag may be
# absent).
check_version() {
  local name=$1 tool=${2:-$1}
  if ! command -v "$tool" >/dev/null 2>&1; then
    fail "$name" "not in PATH: $tool"
    return
  fi
  local out rc
  if out=$($TCMD "$tool" --version 2>&1); then rc=0; else rc=$?; fi
  if [ "$rc" -eq 124 ] || [ "$rc" -eq 125 ]; then
    fail "$name" "timed out after 20s (--version)"
    return
  fi
  if [ "$rc" -eq 0 ]; then
    first=$(printf '%s\n' "$out" | sed -n '1p')
    case "$first" in
      *[![:space:]]*) : ;;
      *) first=$(printf '%s\n' "$out" | grep -m1 -v '^[[:space:]]*$' || true) ;;
    esac
    if [ -n "$first" ]; then
      echo "ok $name: $first"
      return
    fi
    echo "ok $name: --version exit 0 (no version line)"
    return
  fi
  # --version non-zero: the tool may lack --version (the spec accepts that):
  # fall back to --help, accept exit 0. (Exit codes vary: argparse uses 2,
  # others 1, so any non-zero gets the fallback.)
  if out=$($TCMD "$tool" --help 2>&1); then
    first=$(printf '%s\n' "$out" | grep -m1 -v '^[[:space:]]*$' || true)
    echo "ok $name: --version unsupported, --help exit 0${first:+: $first}"
    return
  fi
  fail "$name" "--version rc=$rc: $(printf '%s\n' "$out" | sed -n '1p')"
}

# check_exec <name> <path> -- the path must be an executable file (no run).
check_exec() {
  local name=$1 path=$2
  if [ -x "$path" ]; then
    echo "ok $name: executable"
  else
    fail "$name" "not an executable: $path"
  fi
}

# --- role dispatcher ---------------------------------------------------------

# KUBEADP_ROLE unset or bogus must exit 2 (documented contract of the
# src/kubeadp-agent dispatcher). Runs as the image user; no config file is
# mounted, so the dispatcher errors on the role BEFORE any config processing
# that could need one.
if ! command -v kubeadp-agent >/dev/null 2>&1; then
  fail "kubeadp-agent-role-exit2" "not in PATH: kubeadp-agent"
else
  kubeadp_rc=0
  kubeadp_out=$($TCMD env KUBEADP_ROLE=bogus kubeadp-agent 2>&1) || kubeadp_rc=$?
  if [ "$kubeadp_rc" -ne 2 ]; then
    fail "kubeadp-agent-role-exit2" "expected exit 2 on KUBEADP_ROLE=bogus, got $kubeadp_rc: $(printf '%s\n' "$kubeadp_out" | sed -n '1p')"
  else
    echo "ok kubeadp-agent-role-exit2: exit 2 on KUBEADP_ROLE=bogus"
  fi
fi

# --- harness engines + CLI tools --------------------------------------------

check_version claude
check_version node
check_version npm
check_version npx
check_version tini
check_version ttyd
check git 'git --version'
check curl 'curl --version'
check gcc 'gcc --version'
check make 'make --version'
check pkg-config 'pkg-config --version'
# agy at the core's default agy_path: executable plus --version where it is
# supported (it is a Node-backed CLI; --version may fall back to --help).
check_version agy /usr/local/libexec/agy

# agent-run comes with the image (workers reach jobs).
# kubeadp-agent orchestrator --help must print its usage and exit 0.
check_version agent-run
check_version opencode
check kubeadp-agent-orchestrator-help 'kubeadp-agent orchestrator --help'

# --- worker role boot check ----------------------------------
# Same bounded boot check as docker/agent-run-api/smoke-test.sh: the API server
# must answer /healthz on a loopback port within ~5s.
if command -v kubeadp-agent >/dev/null 2>&1; then
  worker_bin="kubeadp-agent"
else
  worker_bin=""
fi
if [ -z "$worker_bin" ]; then
  fail "worker-boot" "not executable: kubeadp-agent"
else
  ara_work=$(mktemp -d)
  ara_state=$(mktemp -d)
  ara_port=$($TCMD python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()' 2>/dev/null || echo $((20000 + RANDOM % 10000)))
  # The A2A surface requires a jobset root key (jobset-only authorization).
  ara_rootkey="$ara_state/jobset-root-key"
  head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$ara_rootkey"
  chmod 0400 "$ara_rootkey"
  KUBEADP_ROLE=worker \
  KUBEADP_API_ALLOW_UNAUTHENTICATED=1 \
  KUBEADP_API_ALLOW_OPEN=1 \
  KUBEADP_JOBSET_ROOT_KEY_FILE="$ara_rootkey" \
  AGENT_RUN_API_LISTEN="127.0.0.1:$ara_port" \
  AGENT_RUN_WORKSPACE_ROOT="$ara_work" \
  AGENT_RUN_STATE_ROOT="$ara_state" \
  "$worker_bin" >/dev/null 2>&1 &
  ara_pid=$!

  boot_ok=0
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if out=$($TCMD curl -s -f --max-time 1 "http://127.0.0.1:$ara_port/healthz" 2>/dev/null); then
      if printf '%s\n' "$out" | grep -q '"status"'; then
        boot_ok=1
        break
      fi
    fi
    if ! kill -0 "$ara_pid" 2>/dev/null; then
      break
    fi
    sleep 0.5
  done

  kill -TERM "$ara_pid" 2>/dev/null || true
  for _ in 1 2 3 4 5 6; do
    if ! kill -0 "$ara_pid" 2>/dev/null; then
      break
    fi
    sleep 0.5
  done
  if kill -0 "$ara_pid" 2>/dev/null; then
    kill -KILL "$ara_pid" 2>/dev/null || true
  fi
  wait "$ara_pid" 2>/dev/null || true
  rm -rf "$ara_work" "$ara_state"

  if [ "$boot_ok" -eq 1 ]; then
    echo "ok worker-boot: healthz responded on 127.0.0.1:$ara_port"
  else
    fail "worker-boot" "failed to answer on 127.0.0.1:$ara_port within 5s"
  fi
fi

# --- ttyd (orchestrator sidecar) ---------------------------------------------
# Verification that a stripped minimal rootfs actually serves: ttyd v1.7.7 is
# buildroot-static with its own libwebsockets/OpenSSL statically linked (no
# /usr/lib deps); if the /usr/bin/python3 symlink or a similarly tiny detail
# were to break it at runtime it would refuse to launch. Start it on an
# ephemeral port and confirm the HTTP responses (a ttyd that cannot serve the
# web terminal would break the orchestrator pod's interactive shell).
if ! command -v ttyd >/dev/null 2>&1; then
  fail "ttyd-serve" "not in PATH: ttyd"
else
  ttyd_port=$((21000 + RANDOM % 1000))
  ttyd --port "$ttyd_port" --interface 127.0.0.1 --max-clients 1 bash -l >/dev/null 2>&1 &
  ttyd_pid=$!
  ttyd_ok=0
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if $TCMD curl -s -f --max-time 1 "http://127.0.0.1:$ttyd_port/" >/dev/null 2>&1; then
      ttyd_ok=1
      break
    fi
    if ! kill -0 "$ttyd_pid" 2>/dev/null; then
      break
    fi
    sleep 0.3
  done
  kill -TERM "$ttyd_pid" 2>/dev/null || true
  for _ in 1 2 3 4 5 6; do
    if ! kill -0 "$ttyd_pid" 2>/dev/null; then
      break
    fi
    sleep 0.3
  done
  if kill -0 "$ttyd_pid" 2>/dev/null; then
    kill -KILL "$ttyd_pid" 2>/dev/null || true
  fi
  wait "$ttyd_pid" 2>/dev/null || true
  if [ "$ttyd_ok" -eq 1 ]; then
    echo "ok ttyd-serve: served HTTP on 127.0.0.1:$ttyd_port"
  else
    fail "ttyd-serve" "failed to serve HTTP on 127.0.0.1:$ttyd_port"
  fi
fi

# --- dangling symlink scan ---------------------------------------------------
# The 2026.09.10 claude failure mode, generalized: `cp -a` staging kept the
# symlink but not its target, so it dangled in the final image. Any dangling
# link in the tool trees fails the build (as in both old smoke tests; the
# ai-server tree also scanned /home/coder, this image's home is /home/agent).
out=$(find /usr/local /opt /home/agent -xtype l 2>/dev/null | sort)
if [ -z "$out" ]; then
  echo "ok symlinks: no dangling symlinks in /usr/local /opt /home/agent"
else
  fail "symlinks" "dangling: $(printf '%s\n' "$out" | sed -n '1,20p' | tr '\n' ' ')"
fi
out=$(find /usr/bin /bin -xtype l 2>/dev/null | sort)
if [ -z "$out" ]; then
  echo "ok symlinks: no dangling symlinks in /usr/bin /bin"
else
  fail "symlinks" "dangling: $(printf '%s\n' "$out" | sed -n '1,20p' | tr '\n' ' ')"
fi

if [ "$FAILS" -gt 0 ]; then
  echo "smoke check FAILED: $FAILS failed check(s)"
  exit 1
fi
echo "smoke check passed"
exit 0
