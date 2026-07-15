#!/bin/bash
# MeshØ DigitalOcean Automated Deployment Script

set -e

echo "========================================="
echo "     MeshØ DigitalOcean Deployment Bot   "
echo "========================================="

# Ask for server configuration if not set in environment
if [ -z "$SERVER_IP" ]; then
    read -p "Enter your DigitalOcean Droplet IP: " SERVER_IP
fi

if [ -z "$DOMAIN" ]; then
    read -p "Enter your Domain Name (e.g. api.meshzero.network) [press Enter to skip SSL]: " DOMAIN
fi

SSH_USER=${SSH_USER:-root}
PORT=8080

echo "-----------------------------------------"
echo "⚙️  1. Compiling Go Broker for Linux AMD64..."
echo "-----------------------------------------"
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/mesh-zero-linux cmd/mesh-zero/main.go
echo "✓ Compilation successful."

echo "-----------------------------------------"
echo "🚀 2. Transferring binary to $SERVER_IP..."
echo "-----------------------------------------"
ssh $SSH_USER@$SERVER_IP "mkdir -p /var/lib/mesh-zero"
scp dist/mesh-zero-linux $SSH_USER@$SERVER_IP:/usr/local/bin/mesh-zero
echo "✓ Transfer complete."

echo "-----------------------------------------"
echo "🔧 3. Configuring Remote Systemd Service..."
echo "-----------------------------------------"
ssh $SSH_USER@$SERVER_IP "cat << 'EOF' > /etc/systemd/system/mesh-zero.service
[Unit]
Description=Mesh-Zero Central Broker
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/var/lib/mesh-zero
ExecStart=/usr/local/bin/mesh-zero broker start 8080
Restart=always
RestartSec=5
Environment=PORT=8080

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now mesh-zero
systemctl restart mesh-zero
"
echo "✓ Systemd service configured and running."

if [ -n "$DOMAIN" ]; then
    echo "-----------------------------------------"
    echo "🌐 4. Configuring Caddy Reverse Proxy & SSL..."
    echo "-----------------------------------------"
    ssh $SSH_USER@$SERVER_IP "
    if ! command -v caddy &> /dev/null; then
        echo 'Installing Caddy...'
        apt-get update -y
        apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list
        apt-get update -y
        apt-get install -y caddy
    fi

    echo 'Configuring Caddyfile...'
    cat << EOF2 > /etc/caddy/Caddyfile
$DOMAIN {
    reverse_proxy localhost:8080
}
EOF2
    systemctl reload caddy
    "
    echo "✓ Caddy reverse proxy and automatic SSL configured!"
fi

echo "========================================="
echo "🎉 MeshØ Broker successfully deployed!   "
echo "URL: http://$SERVER_IP"
if [ -n "$DOMAIN" ]; then
    echo "Secure URL: https://$DOMAIN"
fi
echo "========================================="
