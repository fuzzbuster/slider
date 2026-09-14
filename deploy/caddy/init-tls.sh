#!/bin/sh
set -eu

server_dir="${SERVER_TLS_DIR:-/server-tls}"
trust_dir="${TRUST_TLS_DIR:-/trust-tls}"
server_key="${server_dir}/server.key"
server_cert="${server_dir}/server.crt"
ca_cert="${trust_dir}/ca.crt"

mkdir -p "$server_dir" "$trust_dir"
umask 077

if [ -s "$server_key" ] && [ -s "$server_cert" ] && [ -s "$ca_cert" ]; then
	step certificate verify "$server_cert" --roots "$ca_cert" --host slider
	cert_public_key="$(step crypto key public "$server_cert")"
	key_public_key="$(step crypto key public "$server_key")"
	if [ "$cert_public_key" != "$key_public_key" ]; then
		echo "existing Slider TLS certificate and key do not match" >&2
		exit 1
	fi
	echo "using existing Slider internal TLS certificate"
	exit 0
fi

if [ -e "$server_key" ] || [ -e "$server_cert" ] || [ -e "$ca_cert" ]; then
	echo "Slider TLS volumes contain an incomplete certificate set" >&2
	exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

step certificate create \
	"Slider Internal CA" \
	"${tmp_dir}/ca.crt" \
	"${tmp_dir}/ca.key" \
	--profile root-ca \
	--kty EC \
	--curve P-256 \
	--no-password \
	--insecure \
	--not-after 87600h
step certificate create \
	slider \
	"${tmp_dir}/server.crt" \
	"${tmp_dir}/server.key" \
	--profile leaf \
	--kty EC \
	--curve P-256 \
	--ca "${tmp_dir}/ca.crt" \
	--ca-key "${tmp_dir}/ca.key" \
	--san slider \
	--no-password \
	--insecure \
	--not-after 87600h

step certificate verify "${tmp_dir}/server.crt" --roots "${tmp_dir}/ca.crt" --host slider
cert_public_key="$(step crypto key public "${tmp_dir}/server.crt")"
key_public_key="$(step crypto key public "${tmp_dir}/server.key")"
if [ "$cert_public_key" != "$key_public_key" ]; then
	echo "generated Slider TLS certificate and key do not match" >&2
	exit 1
fi

cp "${tmp_dir}/server.key" "$server_key"
cp "${tmp_dir}/server.crt" "$server_cert"
cp "${tmp_dir}/ca.crt" "$ca_cert"
chmod 0640 "$server_key"
chmod 0644 "$server_cert" "$ca_cert"
chown 10001:0 "$server_key"
chown 10001:10001 "$server_cert"

echo "generated Slider internal TLS certificate"
