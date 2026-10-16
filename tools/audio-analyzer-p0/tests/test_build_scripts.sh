#!/bin/sh
# run-p0.sh / build/install-deps.sh 的离线测试：用桩命令替代 docker、apt-get、pip、ffmpeg，
# 不需要 Docker、不联网。检查代理传递方式、凭证不外泄、临时文件清理、参数保留。
# 用法：sh tests/test_build_scripts.sh
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
FAILS=0
PASSES=0

ok() { PASSES=$((PASSES + 1)); }
fail() { FAILS=$((FAILS + 1)); echo "FAIL: $*"; }
assert_has() { grep -qxF -- "$2" "$1" && ok || fail "$3（缺少：$2）"; }
assert_not_has() { grep -qxF -- "$2" "$1" && fail "$3（不应出现：$2）" || ok; }
assert_no_text() { grep -qF -- "$2" "$1" && fail "$3（泄露：$2）" || ok; }

# ---------- 桩命令 ----------
mkdir -p "$WORK/bin"
cat > "$WORK/bin/docker" <<'EOF'
#!/bin/sh
log="$STUB_LOG"
echo "CALL $1" >> "$log"
for a in "$@"; do echo "ARG $a" >> "$log"; done
echo "ENV DOCKER_BUILDKIT=${DOCKER_BUILDKIT:-}" >> "$log"
echo "ENV HTTP_PROXY=${HTTP_PROXY:-}" >> "$log"
echo "ENV http_proxy=${http_proxy:-}" >> "$log"
echo "ENV HTTPS_PROXY=${HTTPS_PROXY:-}" >> "$log"
echo "ENV NO_PROXY=${NO_PROXY:-}" >> "$log"
for a in "$@"; do
  case "$a" in
    id=build_proxy,src=*)
      f="${a#id=build_proxy,src=}"
      echo "SECRET_FILE $f" >> "$log"
      echo "SECRET_CONTENT $(cat "$f")" >> "$log"
      echo "SECRET_MODE $(stat -c %a "$f" 2>/dev/null || stat -f %Lp "$f")" >> "$log"
      ;;
  esac
done
exit 0
EOF
chmod +x "$WORK/bin/docker"

run_p0() {  # $1 = 用例名，其余为传给 run-p0.sh 的参数；环境变量由调用方设置
  name="$1"; shift
  STUB_LOG="$WORK/$name.log"; export STUB_LOG
  : > "$STUB_LOG"
  PATH="$WORK/bin:$PATH" MUSIC="$WORK/music" OUT="$WORK/out-$name" MODELS="${MODELS_DIR:-$WORK/models-$name}" \
    sh "$ROOT/run-p0.sh" "$@" > "$WORK/$name.out" 2>&1
  echo $? > "$WORK/$name.rc"
}
clean_env() { unset HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy PROXY_FILE PROXY_MODE RUNTIME_PROXY MODELS_DIR 2>/dev/null || true; }
mkdir -p "$WORK/music"

# ---------- 1. 无代理：构建方式不变，参数原样保留 ----------
clean_env
run_p0 noproxy --include 'a b' --sample 5
L="$WORK/noproxy.log"
assert_has "$L" "ARG -f" "noproxy 使用 -f"
assert_has "$L" "ARG $ROOT/Dockerfile" "noproxy 使用默认 Dockerfile"
assert_not_has "$L" "ARG --build-arg" "noproxy 不应有代理构建参数"
assert_not_has "$L" "ARG --secret" "noproxy 不应有 secret"
assert_has "$L" "ARG $WORK/music:/music:ro" "音乐目录必须只读挂载"
assert_has "$L" "ARG --cpus" "保留 CPU 限制"
assert_has "$L" "ARG --memory" "保留内存限制"
assert_has "$L" "ARG 2g" "内存上限 2g"
assert_has "$L" "ARG a b" "带空格的用户参数必须原样保留"
assert_has "$L" "ARG --sample" "用户参数 --sample 保留"
assert_not_has "$L" "ARG HTTP_PROXY" "noproxy 运行期不应传代理"
tail -n 20 "$L" | grep -q "ARG 5" && ok || fail "用户参数应位于 docker run 末尾"

# ---------- 2. 普通代理：预定义构建参数，只传名字 ----------
clean_env
export HTTP_PROXY="http://10.9.8.7:3128"
run_p0 args
L="$WORK/args.log"; O="$WORK/args.out"
for v in HTTP_PROXY HTTPS_PROXY http_proxy https_proxy; do
  assert_has "$L" "ARG $v" "args 模式应以名字传递 $v"
done
assert_has "$L" "ARG $ROOT/Dockerfile" "args 模式用默认 Dockerfile"
assert_not_has "$L" "ARG --secret" "args 模式不应用 secret"
grep "^ARG " "$L" | grep -qF "10.9.8.7" && fail "代理地址不应出现在命令行参数中" || ok
assert_has "$L" "ENV http_proxy=http://10.9.8.7:3128" "小写 http_proxy 应导出给 docker CLI（apt 只认小写）"
assert_has "$L" "ENV HTTPS_PROXY=http://10.9.8.7:3128" "只配置 HTTP_PROXY 时 HTTPS_PROXY 应共用"
assert_no_text "$O" "10.9.8.7" "脚本输出不应打印代理地址"
assert_has "$L" "ARG -e" "模型未缓存时运行期应传代理"

# 2b. 模型已缓存：运行期不再传代理
mkdir -p "$WORK/models-cached"; echo x > "$WORK/models-cached/discogs-effnet-bsdynamic-1.onnx"
MODELS_DIR="$WORK/models-cached" run_p0 args-cached
assert_not_has "$WORK/args-cached.log" "ARG -e" "模型已缓存时运行期不应传代理"

# 2c. NO_PROXY 同样以名字传递
export NO_PROXY="localhost,192.168.0.0/16"
run_p0 args-noproxy
assert_has "$WORK/args-noproxy.log" "ARG NO_PROXY" "NO_PROXY 以名字传递"
assert_has "$WORK/args-noproxy.log" "ARG no_proxy" "no_proxy 以名字传递"

# ---------- 3. 带凭证：自动改用 BuildKit secret，临时文件结束后删除 ----------
clean_env
export HTTPS_PROXY="http://user:s3cr3t@10.9.8.7:3128"
run_p0 cred
L="$WORK/cred.log"; O="$WORK/cred.out"
assert_has "$L" "ARG $ROOT/Dockerfile.buildkit" "含凭证时应使用 Dockerfile.buildkit"
assert_has "$L" "ARG --secret" "含凭证时应使用 --secret"
assert_has "$L" "ENV DOCKER_BUILDKIT=1" "secret 模式应启用 BuildKit"
assert_has "$L" "SECRET_CONTENT http://user:s3cr3t@10.9.8.7:3128" "secret 文件内容正确"
assert_has "$L" "SECRET_MODE 600" "临时 secret 文件权限应为 600"
grep "^ARG " "$L" | grep -qF "s3cr3t" && fail "凭证不应出现在命令行参数中" || ok
assert_not_has "$L" "ARG HTTPS_PROXY" "secret 模式不应再用构建参数传代理"
assert_has "$L" "ENV HTTP_PROXY=" "secret 模式不应把代理导出给 docker CLI"
assert_no_text "$O" "s3cr3t" "脚本输出不应打印凭证"
grep "^ARG .*:/run/secrets/proxy_url:ro$" "$L" >/dev/null && ok || fail "运行期应以只读文件挂载代理"
f="$(grep '^SECRET_FILE ' "$L" | head -n1 | cut -d' ' -f2-)"
[ -n "$f" ] && [ ! -e "$f" ] && ok || fail "临时 secret 文件应在结束后删除：$f"

# ---------- 4. PROXY_FILE：直接使用用户文件，不复制、不删除 ----------
clean_env
printf 'http://u:p@10.9.8.7:3128\n' > "$WORK/proxy.txt"; chmod 600 "$WORK/proxy.txt"
PROXY_FILE="$WORK/proxy.txt" run_p0 file
assert_has "$WORK/file.log" "SECRET_FILE $WORK/proxy.txt" "PROXY_FILE 应直接作为 secret 源"
[ -e "$WORK/proxy.txt" ] && ok || fail "不应删除用户提供的 PROXY_FILE"

# ---------- 5. PROXY_MODE=off 与错误输入 ----------
clean_env
export HTTP_PROXY="http://10.9.8.7:3128"
PROXY_MODE=off run_p0 off
assert_not_has "$WORK/off.log" "ARG HTTP_PROXY" "PROXY_MODE=off 不应传代理"
clean_env
PROXY_MODE=args run_p0 badargs
[ "$(cat "$WORK/badargs.rc")" = 2 ] && ok || fail "PROXY_MODE=args 但无代理应退出码 2"
PROXY_FILE="$WORK/nope" run_p0 badfile
[ "$(cat "$WORK/badfile.rc")" = 2 ] && ok || fail "PROXY_FILE 不可读应退出码 2"
SKIP_BUILD=1 run_p0 skip
grep -q "^CALL build" "$WORK/skip.log" && fail "SKIP_BUILD=1 不应构建" || ok
BUILD_NO_CACHE=1 BUILD_NETWORK=host APT_MIRROR=https://mirror.example run_p0 opts
assert_has "$WORK/opts.log" "ARG --no-cache" "BUILD_NO_CACHE=1 应传 --no-cache"
assert_has "$WORK/opts.log" "ARG --network" "BUILD_NETWORK 应传 --network"
assert_has "$WORK/opts.log" "ARG APT_MIRROR=https://mirror.example" "APT_MIRROR 应作为构建参数传入"

# ---------- 6. 两个 Dockerfile 除依赖安装步骤外必须一致 ----------
tail_of() { sed -n '/^WORKDIR \/app/,$p' "$1"; }
[ "$(tail_of "$ROOT/Dockerfile")" = "$(tail_of "$ROOT/Dockerfile.buildkit")" ] && ok || fail "Dockerfile 与 Dockerfile.buildkit 的后半部分不一致"
grep -Eq '^ARG +(HTTP|HTTPS|NO)_PROXY|^ARG +(http|https|no)_proxy' "$ROOT/Dockerfile" "$ROOT/Dockerfile.buildkit" \
  && fail "不应声明代理 ARG（声明后会写入镜像历史）" || ok
grep -qiE 'ENV +(HTTP|HTTPS)_PROXY' "$ROOT/Dockerfile" "$ROOT/Dockerfile.buildkit" && fail "不应用 ENV 设置代理" || ok

# ---------- 7. install-deps.sh：代理生效但不打印、不落盘 ----------
mkdir -p "$WORK/ibin" "$WORK/build" "$WORK/lists"
cp "$ROOT/build/install-deps.sh" "$ROOT/requirements.txt" "$WORK/build/"
cat > "$WORK/ibin/apt-get" <<'EOF'
#!/bin/sh
echo "APT http_proxy=${http_proxy:-} https_proxy=${https_proxy:-} args=$*" >> "$STUB_LOG"
EOF
cat > "$WORK/ibin/pip" <<'EOF'
#!/bin/sh
echo "PIP HTTPS_PROXY=${HTTPS_PROXY:-} INDEX=${PIP_INDEX_URL-<unset>} args=$*" >> "$STUB_LOG"
EOF
printf '#!/bin/sh\nexit 0\n' > "$WORK/ibin/ffmpeg"
chmod +x "$WORK/ibin/"*

install_run() {  # $1 = 用例名
  STUB_LOG="$WORK/i-$1.log"; export STUB_LOG; : > "$STUB_LOG"
  PATH="$WORK/ibin:$PATH" APT_LISTS_DIR="$WORK/lists" sh "$WORK/build/install-deps.sh" > "$WORK/i-$1.out" 2>&1
  echo $? > "$WORK/i-$1.rc"
}

clean_env
PIP_INDEX_URL="" install_run direct
grep -q "^APT http_proxy= https_proxy= " "$WORK/i-direct.log" && ok || fail "无代理时 apt 不应有代理"
grep -q "Acquire::http::Timeout=30" "$WORK/i-direct.log" && ok || fail "apt 应设置超时"
grep -q "Acquire::Retries=3" "$WORK/i-direct.log" && ok || fail "apt 应设置重试"
grep -q "INDEX=<unset>" "$WORK/i-direct.log" && ok || fail "空 PIP_INDEX_URL 应被移除"
grep -q "代理：未启用" "$WORK/i-direct.out" && ok || fail "应提示未启用代理"

clean_env
HTTP_PROXY="http://10.9.8.7:3128" install_run arg
grep -q "^APT http_proxy=http://10.9.8.7:3128 https_proxy=http://10.9.8.7:3128" "$WORK/i-arg.log" && ok || fail "构建参数代理应导出为 apt 可用的小写变量"
grep -q "^PIP HTTPS_PROXY=http://10.9.8.7:3128" "$WORK/i-arg.log" && ok || fail "pip 应使用代理"
assert_no_text "$WORK/i-arg.out" "10.9.8.7" "install-deps.sh 输出不应打印代理地址"

clean_env
printf 'http://u:s3cr3t@10.9.8.7:3128\n' > "$WORK/secret"
BUILD_PROXY_SECRET_FILE="$WORK/secret" HTTP_PROXY="http://other:1" install_run secret
grep -q "^APT http_proxy=http://u:s3cr3t@10.9.8.7:3128" "$WORK/i-secret.log" && ok || fail "secret 应优先于构建参数"
grep -q "来源：BuildKit secret" "$WORK/i-secret.out" && ok || fail "应提示代理来自 secret"
assert_no_text "$WORK/i-secret.out" "s3cr3t" "install-deps.sh 输出不应打印凭证"

clean_env
APT_FORCE_IPV4=1 install_run ipv4
grep -q "Acquire::ForceIPv4=true" "$WORK/i-ipv4.log" && ok || fail "APT_FORCE_IPV4=1 应生效"

echo "通过 $PASSES 项，失败 $FAILS 项"
[ "$FAILS" -eq 0 ]
