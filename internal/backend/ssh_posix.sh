# Parsed inside relay_main by the controller. Uses only system shell utilities.
relay_encode() { base64 | tr -d '\r\n'; }
relay_failure() {
    printf 'AgentRelaySSH1\nstatus\t%s\nerror\t' "$1"
    printf '%s' "$2" | relay_encode
    printf '\n'
    exit 0
}
relay_inside() {
    [ "$relay_root" = / ] && return 0
    [ "$1" = "$relay_root" ] && return 0
    case "$1" in "$relay_root"/*) return 0;; *) return 1;; esac
}
for relay_tool in sh base64 tr uname dirname basename wc head mktemp mkfifo env ps awk sleep cat rm; do
    command -v "$relay_tool" >/dev/null 2>&1 || relay_failure 501 "required system utility is unavailable: $relay_tool"
done
ps -eo pid=,ppid= >/dev/null 2>&1 || relay_failure 501 'system ps lacks process-tree support'
relay_root=$(cd -P "$relay_root" 2>/dev/null && pwd -P) || relay_failure 400 'authorized workspace must exist'
case "$relay_operation" in
probe)
    relay_os=$(uname -s)
    case "$relay_os" in Darwin) relay_os=darwin;; Linux) relay_os=linux;; *) relay_failure 501 'unsupported POSIX platform';; esac
    relay_arch=$(uname -m)
    case "$relay_arch" in x86_64) relay_arch=amd64;; aarch64|arm64) relay_arch=arm64;; esac
    printf 'AgentRelaySSH1\nstatus\t200\nos\t'; printf '%s' "$relay_os" | relay_encode
    printf '\narchitecture\t'; printf '%s' "$relay_arch" | relay_encode; printf '\n'
    return
    ;;
read)
    relay_parent=$(cd -P "$relay_root/$(dirname "$relay_path")" 2>/dev/null && pwd -P) || relay_failure 404 'file directory does not exist'
    relay_inside "$relay_parent" || relay_failure 400 'file escapes authorized workspace'
    relay_file="$relay_parent/$(basename "$relay_path")"
    [ ! -L "$relay_file" ] || relay_failure 400 'symbolic link file is not allowed'
    [ -f "$relay_file" ] || relay_failure 404 'file does not exist or is not regular'
    relay_size=$(wc -c < "$relay_file") || relay_failure 400 'cannot read file'
    [ "$relay_size" -le 16777216 ] || relay_failure 400 'file read exceeds 16 MiB'
    printf 'AgentRelaySSH1\nstatus\t200\ncontent\t'
    # Bound the stream even if the file grows after the size check.
    head -c 16777217 "$relay_file" | relay_encode
    printf '\n'
    return
    ;;
exec) ;;
*) relay_failure 501 'unsupported SSH operation';;
esac
relay_cwd=$(cd -P "$relay_root/$relay_path" 2>/dev/null && pwd -P) || relay_failure 400 'working directory does not exist'
relay_inside "$relay_cwd" || relay_failure 400 'working directory escapes authorized workspace'
relay_temp=$(mktemp -d "${TMPDIR:-/tmp}/agentrelay-ssh.XXXXXXXX") || relay_failure 500 'cannot create execution staging directory'
relay_pid= relay_timer= relay_watch= relay_out_reader= relay_err_reader=
# Freeze each running process before collecting its children, then terminate
# the tree. Detached processes are outside the synchronous shell lifecycle.
relay_kill_tree() {
    kill -STOP "$1" 2>/dev/null || return 0
    relay_children=$(ps -eo pid=,ppid= | awk -v parent="$1" '$2 == parent { print $1 }')
    for relay_child in $relay_children; do relay_kill_tree "$relay_child"; done
    kill -KILL "$1" 2>/dev/null || :
}
relay_cleanup() {
    trap '' TERM HUP INT
    if [ -n "$relay_pid" ]; then relay_kill_tree "$relay_pid"; fi
    for relay_cleanup_pid in "$relay_timer" "$relay_watch" "$relay_out_reader" "$relay_err_reader"; do
        if [ -n "$relay_cleanup_pid" ]; then relay_kill_tree "$relay_cleanup_pid"; wait "$relay_cleanup_pid" 2>/dev/null || :; fi
    done
    rm -rf "$relay_temp"
}
relay_interrupt() {
    if [ -n "$relay_pid" ]; then relay_kill_tree "$relay_pid"; fi
}
trap relay_cleanup EXIT
trap relay_interrupt TERM HUP INT
printf '%s\n' "$relay_command" > "$relay_temp/command"
mkfifo "$relay_temp/out.pipe" "$relay_temp/err.pipe" || relay_failure 500 'cannot create output pipes'
# Retain at most limit+1 bytes and continue draining the pipes. Disk usage does
# not grow with command output; the extra byte reports truncation.
(head -c 1048577 > "$relay_temp/out"; cat > /dev/null) < "$relay_temp/out.pipe" &
relay_out_reader=$!
(head -c 1048577 > "$relay_temp/err"; cat > /dev/null) < "$relay_temp/err.pipe" &
relay_err_reader=$!
cd "$relay_cwd" || relay_failure 400 'cannot enter working directory'
relay_env_run < /dev/null > "$relay_temp/out.pipe" 2> "$relay_temp/err.pipe" &
relay_pid=$!
relay_parent=$$
(sleep "$relay_timeout"; : > "$relay_temp/timed_out"; kill -TERM "$relay_parent" 2>/dev/null) &
relay_timer=$!
exec 3<&0
(IFS= read -r relay_extra <&3 || kill -TERM "$relay_parent" 2>/dev/null) &
relay_watch=$!
wait "$relay_pid"
relay_code=$?
# An interrupted wait does not reap the terminated child immediately.
if [ "$relay_code" -ge 128 ]; then wait "$relay_pid" 2>/dev/null || :; fi
relay_pid=
relay_kill_tree "$relay_timer"; relay_timer=
# Descendants retaining a pipe after the command exits cannot hang the RPC.
(sleep 2; relay_kill_tree "$relay_out_reader"; relay_kill_tree "$relay_err_reader") &
relay_drain_timer=$!
wait "$relay_out_reader" 2>/dev/null || :
wait "$relay_err_reader" 2>/dev/null || :
relay_kill_tree "$relay_drain_timer"; wait "$relay_drain_timer" 2>/dev/null || :
relay_out_reader= relay_err_reader=
relay_timed_out=0; [ ! -f "$relay_temp/timed_out" ] || relay_timed_out=1
relay_truncated=0
[ "$(wc -c < "$relay_temp/out")" -le 1048576 ] || relay_truncated=1
[ "$(wc -c < "$relay_temp/err")" -le 1048576 ] || relay_truncated=1
printf 'AgentRelaySSH1\nstatus\t200\nexit_code\t%s\ntimed_out\t%s\ntruncated\t%s\nstdout\t' "$relay_code" "$relay_timed_out" "$relay_truncated"
head -c 1048576 "$relay_temp/out" | relay_encode
printf '\nstderr\t'; head -c 1048576 "$relay_temp/err" | relay_encode; printf '\n'
