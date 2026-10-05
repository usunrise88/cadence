# shellcheck shell=sh
# Server kind triton: Triton Inference Server's HTTP model-control API, the server started with
# --model-control-mode=explicit and a model repository at the target's repositoryPath. Each function takes the model
# name (the directory under the repository; its config.pbtxt names no other) and reaches $SERVER_URL on this host.
server_load() {
	curl -fsS -X POST "$SERVER_URL/v2/repository/models/$1/load" >/dev/null
}
server_ready() {
	curl -fsS -o /dev/null "$SERVER_URL/v2/models/$1/ready"
}
server_unload() {
	curl -fsS -X POST "$SERVER_URL/v2/repository/models/$1/unload" >/dev/null
}
