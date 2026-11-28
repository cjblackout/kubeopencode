#!/bin/bash
# opencode engine smoke test against the mock LLM (same pod, localhost).
export HOME=/work/home

# ==============================================================================
# The kubeadp-agent image bundles the opencode CLI engine at the image's
# /usr/local/bin/opencode (KA-16/KA-17); no init container provisions /tools any
# more, so the engine is invoked from its bundled path.
# ==============================================================================
mkdir -p "$HOME/.config/opencode" /workspace/proj /workspace/project-a /work/state
# ==============================================================================

res=0; ok() { echo "SMOKE PASS $*"; }; bad() { echo "SMOKE FAIL $*"; res=1; }
git config --global user.email smoke@example.invalid; git config --global user.name smoke
(cd /workspace/proj && git init -q -b main && echo hello > README.md && git add -A && git commit -q -m init) || bad fixture-repo
cat > "$HOME/.config/opencode/opencode.json" <<'J'
{"$schema":"https://opencode.ai/config.json","autoupdate":false,"share":"disabled",
 "model":"mock/mock-1","small_model":"mock/mock-1",
 "provider":{"mock":{"npm":"@ai-sdk/openai-compatible","name":"mock","options":{"baseURL":"http://127.0.0.1:8080/v1","apiKey":"x"},
  "models":{"mock-1":{"name":"mock-1","limit":{"context":8192,"output":1024}}}}}}
J
for i in $(seq 1 60); do curl -sf http://127.0.0.1:8080/v1/models >/dev/null && break; sleep 1; done
curl -sf http://127.0.0.1:8080/v1/models >/dev/null && ok mock-up || { bad mock-up; exit 1; }
echo "== step 1: plain opencode run against the mock =="
cd /workspace/proj
timeout 120 /usr/local/bin/opencode run --format json -m mock/mock-1 "say hi" > /work/oc-direct.log 2>&1; echo "opencode exit=$?"
head -c 1500 /work/oc-direct.log; echo
grep -q 'MOCK-OK' /work/oc-direct.log && ok opencode-direct-reply || bad opencode-direct-reply
grep -q '"type":"step_finish"' /work/oc-direct.log && ok opencode-step-finish || bad opencode-step-finish
echo "== step 2: kubeadp-agent worker =="
head -c 48 /dev/urandom | base64 -w0 > /work/rootkey; chmod 600 /work/rootkey

# TEMPORARY WORKAROUNDS (KA-9): KUBEADP_SANDBOX_HOME
export KUBEADP_JOBSET_ROOT_KEY_FILE=/work/rootkey KUBEADP_ROLE=worker KUBEADP_API_ALLOW_UNAUTHENTICATED=1 AGENT_RUN_STATE_ROOT=/work/state AGENT_RUN_WORKSPACE_ROOT=/workspace AGENT_RUN_OPENCODE_PATH=/usr/local/bin/opencode KUBEADP_SANDBOX_HOME=/work/home

# tiny JSON helper (no jq in the image): jf '<js expr on j>' reads stdin
jf() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);const v=eval(process.argv[1]);console.log(v===undefined?"":(typeof v==="object"?JSON.stringify(v):v))}catch(e){console.log("")}})' "$1"; }
nohup kubeadp-agent worker > /work/worker.log 2>&1 &
for i in $(seq 1 60); do curl -sf http://127.0.0.1:8002/healthz >/dev/null && break; sleep 1; done
curl -sf http://127.0.0.1:8002/healthz >/dev/null && ok worker-up || { bad worker-up; tail -30 /work/worker.log; exit 1; }
API=http://127.0.0.1:8002/api/v1
curl -s $API/capabilities > /work/caps.json
OC=$(jf 'JSON.stringify((j.capabilities||j.engines||[]).find(e=>e.engine==="opencode"))' < /work/caps.json)
echo "opencode capability entry: $OC" | head -c 900; echo
for ch in hook tool_result idle_turn; do echo "$OC" | grep -q "$ch" && ok caps-channel-$ch || bad caps-channel-$ch; done
echo "defaults: $(jf 'JSON.stringify(j.defaults)' < /work/caps.json)"
echo "== step 3: opencode job through the API =="
curl -s -X POST $API/jobs -H 'content-type: application/json' -d '{"project":"proj","prompt":"say hi","engine":"opencode","model":"mock/mock-1","sandbox":false}' > /work/start.json
JID=$(grep -o "Job '[0-9a-f]*'" /work/start.json | head -1 | sed -E "s/Job '([0-9a-f]+)'/\1/")
echo "job id: $JID"; [ -n "$JID" ] && ok job-started || { bad job-started; cat /work/start.json; exit 1; }
curl -s $API/jobs/$JID | head -c 500; echo
st() { curl -s $API/jobs/$1 | jf 'j.data && (j.data.job ? j.data.job.status : j.data.status)'; }
for i in $(seq 1 90); do S=$(st $JID); case "$S" in done|failed|self-terminated|incomplete|quota-exhausted) break;; esac; sleep 2; done
echo "job status=$S"; [ "$S" = done ] && ok job-done || bad job-done-$S
curl -s $API/jobs/$JID/result > /work/result.json; head -c 800 /work/result.json; echo
grep -q 'MOCK-OK' /work/result.json && ok job-result-text || bad job-result-text
echo "--- job log (head) ---"; curl -s $API/jobs/$JID/log | head -c 1200; echo
echo "--- events (SSE, 5s) ---"; timeout 5 curl -sN $API/jobs/$JID/events | head -c 600; echo
echo "== step 4: retry resumes the same conversation =="
curl -s -X POST $API/jobs/$JID/retry -H 'content-type: application/json' -d '{"prompt":"and again"}' | head -c 300; echo
sleep 3
for i in $(seq 1 90); do S2=$(st $JID); case "$S2" in done|failed|self-terminated|incomplete|quota-exhausted) break;; esac; sleep 2; done
[ "$S2" = done ] && ok retry-done || bad retry-done-$S2
echo "mock chat requests so far: $(grep -c 'POST /v1/chat/completions' /work/mock.log)"
echo "== step 5: harness opencode config materialised =="
find "$HOME/.config/opencode" -maxdepth 4 -type f | head -20
node -e 'const j=JSON.parse(require("fs").readFileSync(process.env.HOME+"/.config/opencode/opencode.json","utf8"));console.log("keys:",Object.keys(j).join(","));process.exit((j.mcp&&j.mcp.a2a)?0:1)' && ok mcp-a2a-config || bad mcp-a2a-config
node -e 'const j=JSON.parse(require("fs").readFileSync(process.env.HOME+"/.config/opencode/opencode.json","utf8"));process.exit(j.provider&&j.provider.mock?0:1)' && ok provider-kept || bad provider-kept
echo "== a2a-hook transform on an empty inbox =="
echo '{}' | timeout 20 kubeadp-agent a2a-hook transform; echo "a2a-hook transform exit=$?"
echo "== worker log tail =="; tail -12 /work/worker.log
echo "SMOKE RESULT: $([ $res = 0 ] && echo PASS || echo FAIL)"
