#!/usr/bin/env bash
# 进程级网关冒烟：真实监听端口 + clients.json 认证/权限 + SSH 隧道到既有 CPA。
# 凭据只在本进程内存与环境变量中传递，不写入文件、不输出。
set -uo pipefail

SSH_TARGET="${SSH_TARGET:?}"
IDENTITY="${IDENTITY:?}"
MODEL="${MODEL:-gpt-6.1-sol}"
WORK="${WORK:-/tmp/airouter-smoke}"
BIN="${BIN:-/tmp/airouter.exe}"
PORT="${PORT:-18080}"
TUNNEL_PORT="${TUNNEL_PORT:-18317}"
# CPA 配置文件在服务器上的绝对路径；按实际部署填写
CPA_CONFIG="${CPA_CONFIG:-/path/to/cliproxyapi/data/config.yaml}"

SSH=(ssh -i "$IDENTITY" -o BatchMode=yes -o IdentitiesOnly=yes
     -o StrictHostKeyChecking=yes -o HostKeyAlgorithms=ssh-ed25519 -o ConnectTimeout=12)

cleanup() {
  [ -n "${GW_PID:-}" ] && kill "$GW_PID" 2>/dev/null
  [ -n "${TUN_PID:-}" ] && kill "$TUN_PID" 2>/dev/null
  wait 2>/dev/null
}
trap cleanup EXIT

# 1. 从服务器读取既有数据面 Key（不打印、不落盘）
REMOTE_SRC="import pathlib,yaml,sys; c=yaml.safe_load(pathlib.Path('$CPA_CONFIG').read_text()); k=next((v for v in c.get('access',{}).get('api-keys',[]) if isinstance(v,str) and v),''); sys.stdout.write(k); sys.exit(0 if k else 3)"
CPA_KEY="$(printf '%s' "$REMOTE_SRC" | "${SSH[@]}" "$SSH_TARGET" 'python3 -')"
if [ -z "$CPA_KEY" ]; then echo "FAIL: cannot read CPA credential"; exit 2; fi

# 2. 严格主机校验 SSH 隧道
"${SSH[@]}" -N -o ExitOnForwardFailure=yes -L "127.0.0.1:${TUNNEL_PORT}:127.0.0.1:8317" "$SSH_TARGET" &
TUN_PID=$!
for _ in $(seq 1 100); do
  if ! kill -0 "$TUN_PID" 2>/dev/null; then echo "FAIL: tunnel exited"; exit 2; fi
  if (exec 3<>"/dev/tcp/127.0.0.1/${TUNNEL_PORT}") 2>/dev/null; then exec 3>&-; break; fi
  sleep 0.1
done

# 3. 启动独立网关进程
CPA_BASE_URL="http://127.0.0.1:${TUNNEL_PORT}" \
CPA_API_KEY="$CPA_KEY" \
AIROUTER_CLIENTS_FILE="${WORK}/clients.json" \
AIROUTER_LISTEN_ADDR="127.0.0.1:${PORT}" \
AIROUTER_REQUEST_TIMEOUT=120s \
"$BIN" >"${WORK}/gateway.log" 2>&1 &
GW_PID=$!
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done

GW_KEY="$(cat "${WORK}/gateway.key")"
BASE="http://127.0.0.1:${PORT}"
AUTH="Authorization: Bearer ${GW_KEY}"

pass=0; fail=0
check() { # name expected_status actual_status extra
  if [ "$2" = "$3" ]; then echo "PASS  $1 -> $3 ${4:-}"; pass=$((pass+1));
  else echo "FAIL  $1 -> expected $2 got $3 ${4:-}"; fail=$((fail+1)); fi
}

# 4. 健康与认证
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz");        check "GET /healthz" 200 "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/readyz");         check "GET /readyz" 200 "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/models");      check "GET /v1/models no key" 401 "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer wrong" "$BASE/v1/models"); check "GET /v1/models bad key" 401 "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "$AUTH" "$BASE/v1/models"); check "GET /v1/models" 200 "$code"

# 5. 目录只返回允许模型（与 clients.json 的允许列表逐一比对，且不含列表外模型）
models=$(curl -s -H "$AUTH" "$BASE/v1/models")
ids=$(printf '%s' "$models" | python -I -c "import json,sys; print(','.join(sorted(m['id'] for m in json.load(sys.stdin)['data'])))")
expected=$(CLIENTS_JSON="$(cygpath -w "${WORK}/clients.json" 2>/dev/null || echo "${WORK}/clients.json")" python -I -c "import json,os; print(','.join(sorted(json.load(open(os.environ['CLIENTS_JSON']))[0]['allowed_models'])))")
if [ "$ids" = "$expected" ]; then echo "PASS  catalog filtered -> $ids"; pass=$((pass+1));
else echo "FAIL  catalog filter -> got[$ids] expected[$expected]"; fail=$((fail+1)); fi

# 6. 越权模型拒绝（权限文件只允许 $MODEL）
body='{"model":"NOT-ALLOWED-MODEL","messages":[{"role":"user","content":"hi"}]}'
code=$(curl -s -o /dev/null -w '%{http_code}' -H "$AUTH" -H 'Content-Type: application/json' -d "$body" "$BASE/v1/chat/completions")
check "POST chat forbidden model" 403 "$code"

# 7. 三协议普通调用
for spec in \
  "/v1/chat/completions|{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with only OK.\"}],\"max_completion_tokens\":256,\"reasoning_effort\":\"low\"}" \
  "/v1/responses|{\"model\":\"$MODEL\",\"input\":\"Reply with only OK.\",\"store\":false,\"max_output_tokens\":256,\"reasoning\":{\"effort\":\"low\"}}" \
  "/v1/messages|{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with only OK.\"}],\"max_tokens\":256}" ; do
  path="${spec%%|*}"; payload="${spec#*|}"
  out=$(curl -s -w '\n%{http_code}' -H "$AUTH" -H 'Content-Type: application/json' -d "$payload" "$BASE$path")
  code=$(printf '%s' "$out" | tail -n1)
  if [ "$path" = "/v1/messages" ]; then
    out=$(curl -s -w '\n%{http_code}' -H "x-api-key: ${GW_KEY}" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' -d "$payload" "$BASE$path")
    code=$(printf '%s' "$out" | tail -n1)
  fi
  bodyout=$(printf '%s' "$out" | sed '$d')
  model=$(printf '%s' "$bodyout" | python -I -c "import json,sys; print(json.load(sys.stdin).get('model',''))" 2>/dev/null)
  text=$(printf '%s' "$bodyout" | python -I -c "
import json,sys
p=json.load(sys.stdin)
t=''
if 'choices' in p:
    t=(p['choices'][0].get('message') or {}).get('content') or ''
elif 'content' in p:
    t=''.join(x.get('text','') for x in p['content'] if isinstance(x,dict))
elif 'output' in p:
    for it in p['output']:
        for c in it.get('content',[]):
            t+=c.get('text','')
print(t.strip()[:40])
" 2>/dev/null)
  if [ "$code" = "200" ] && [ "$model" = "$MODEL" ] && [ -n "$text" ]; then
    echo "PASS  POST $path plain -> 200 model=$model text=\"$text\""; pass=$((pass+1))
  else
    echo "FAIL  POST $path plain -> code=$code model=$model text=\"$text\""; fail=$((fail+1))
  fi
done

# 8. 三协议流式：检查 SSE 与终止事件
for spec in "/v1/chat/completions|[DONE]" "/v1/responses|response.completed" "/v1/messages|message_stop" ; do
  path="${spec%%|*}"; term="${spec#*|}"
  case "$path" in
    /v1/chat/completions) payload="{\"model\":\"$MODEL\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Reply with only OK.\"}],\"max_completion_tokens\":256,\"reasoning_effort\":\"low\"}";;
    /v1/responses) payload="{\"model\":\"$MODEL\",\"stream\":true,\"input\":\"Reply with only OK.\",\"store\":false,\"max_output_tokens\":256,\"reasoning\":{\"effort\":\"low\"}}";;
    /v1/messages) payload="{\"model\":\"$MODEL\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Reply with only OK.\"}],\"max_tokens\":256}";;
  esac
  if [ "$path" = "/v1/messages" ]; then
    sse=$(curl -sS -m 120 -N -H "x-api-key: ${GW_KEY}" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' -d "$payload" "$BASE$path")
  else
    sse=$(curl -sS -m 120 -N -H "$AUTH" -H 'Content-Type: application/json' -d "$payload" "$BASE$path")
  fi
  if printf "%s" "$sse" | grep -qF "$term"; then
    echo "PASS  POST $path stream -> terminal=$term events=$(printf '%s' "$sse" | grep -c '^data:')"; pass=$((pass+1))
  else
    echo "FAIL  POST $path stream -> terminal=$term missing"; fail=$((fail+1))
  fi
done

echo "-----"
echo "RESULT pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
