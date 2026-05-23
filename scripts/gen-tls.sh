#!/bin/bash
mkdir -p .certs && cd .certs

# 1. Generate Root CA Key & Self-Signed CA Certificate
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
  -keyout ca.key -out ca.crt \
  -subj "/CN=Test-Root-CA"

# 2. Generate Client Private Key & Certificate (For your Relay mTLS)
openssl genrsa -out client.key 2048
openssl req -new -key client.key -out client.csr -subj "/CN=openoutbox-client"
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out client.crt -days 365 -sha256

# 3. Generate Broker Private Key & Certificate WITH SAN (For Kafka itself)
openssl genrsa -out broker.key 2048
openssl req -new -key broker.key -out broker.csr -subj "/CN=localhost"

# Create a temporary config extension file to append SANs
cat <<EOF > v3.ext
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, nonRepudiation, keyEncipherment, dataEncipherment
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
IP.1 = 127.0.0.1
EOF

openssl x509 -req -in broker.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out broker.crt -days 365 -sha256 -extfile v3.ext

cat broker.crt broker.key > broker.pem

# Clean up configuration clutter
rm client.csr broker.csr ca.srl v3.ext
cd ..
echo "🎉 All local PEM certificates generated cleanly in ./.certs/"
