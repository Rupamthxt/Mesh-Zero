#!/bin/bash
# MeshØ DigitalOcean Automated Deployment Script (Unified Broker & Frontend)

set -e

echo "========================================="
echo "   MeshØ DigitalOcean Unified Deployer   "
echo "========================================="

# Ask for server configuration if not set in environment
if [ -z "$SERVER_IP" ]; then
    read -p "Enter your DigitalOcean Droplet IP: " SERVER_IP
fi

if [ -z "$DOMAIN" ]; then
    read -p "Enter your Domain Name (e.g. api.meshzero.network) [press Enter to use HTTP via IP only]: " DOMAIN
fi

SSH_USER=${SSH_USER:-root}
PORT=8080

echo "-----------------------------------------"
echo "⚙️  1. Compiling Go Broker for Linux AMD64..."
echo "-----------------------------------------"
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/mesh-zero-linux cmd/mesh-zero/main.go
echo "✓ Go Broker compilation successful."

echo "-----------------------------------------"
echo "⚛️  2. Building React Frontend (TanStack Start)..."
echo "-----------------------------------------"
WEB_PATH="/Users/rupamthxt/Downloads/emdash-main"
if [ ! -d "$WEB_PATH" ]; then
    echo "❌ Error: Web frontend source folder not found at $WEB_PATH"
    exit 1
fi

cd "$WEB_PATH"
npm run build
cd -

echo "📦 Archiving compiled web assets..."
rm -f dist/web-build.tar.gz
tar -czf dist/web-build.tar.gz -C "$WEB_PATH" .output
echo "✓ Frontend build and archive complete."

echo "-----------------------------------------"
echo "🚀 3. Transferring assets to $SERVER_IP..."
echo "-----------------------------------------"
# Stop services before copying
ssh $SSH_USER@$SERVER_IP "
    systemctl stop mesh-zero >/dev/null 2>&1 || true
    systemctl stop mesh-zero-web >/dev/null 2>&1 || true
    mkdir -p /var/lib/mesh-zero
    mkdir -p /var/lib/mesh-zero-web
"

# Copy Go binary and React build archive
scp dist/mesh-zero-linux $SSH_USER@$SERVER_IP:/usr/local/bin/mesh-zero
scp dist/web-build.tar.gz $SSH_USER@$SERVER_IP:/var/lib/mesh-zero-web/
echo "✓ Transfers complete."

echo "-----------------------------------------"
echo "🔧 4. Configuring Remote Node.js & Services..."
echo "-----------------------------------------"
ssh $SSH_USER@$SERVER_IP "
    # Extract web assets
    cd /var/lib/mesh-zero-web
    tar -xzf web-build.tar.gz
    rm web-build.tar.gz

    # Install Node.js if missing
    if ! command -v node &> /dev/null; then
        echo 'Node.js missing. Installing LTS Node.js...'
        curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
        apt-get install -y nodejs
    fi

    # 1. Setup Go Broker Service
    cat << 'EOF' > /etc/systemd/system/mesh-zero.service
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

    # 2. Setup Node Web Frontend Service
    cat << 'EOF' > /etc/systemd/system/mesh-zero-web.service
[Unit]
Description=Mesh-Zero React Web Frontend
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/var/lib/mesh-zero-web
ExecStart=/usr/bin/node .output/server/index.mjs
Restart=always
RestartSec=5
Environment=PORT=3000

[Install]
WantedBy=multi-user.target
EOF

    # Reload systemd and start daemons
    systemctl daemon-reload
    systemctl enable --now mesh-zero
    systemctl enable --now mesh-zero-web
    systemctl restart mesh-zero
    systemctl restart mesh-zero-web
"
echo "✓ Services successfully configured and running."

echo "-----------------------------------------"
echo "🌐 5. Installing and Configuring Caddy Gateway..."
echo "-----------------------------------------"
ssh $SSH_USER@$SERVER_IP "
    if ! command -v caddy &> /dev/null; then
        echo 'Installing Caddy server...'
        apt-get update -y
        apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list
        apt-get update -y
        apt-get install -y caddy
    fi

    # Determine hosting target
    CADDY_HOST='http://$SERVER_IP'
    if [ -n \"$DOMAIN\" ]; then
        CADDY_HOST=\"$DOMAIN\"
    fi

    echo \"Configuring Caddyfile targeting: \$CADDY_HOST...\"
    cat << EOF > /etc/caddy/Caddyfile
\$CADDY_HOST {
    # Route API and WebSocket connections to the Go Broker (port 8080)
    reverse_proxy /api/* localhost:8080
    reverse_proxy /ws/* localhost:8080

    # Route all standard web traffic to the React web app (port 3000)
    reverse_proxy * localhost:3000
}
EOF
    systemctl restart caddy
"
echo "✓ Caddy reverse proxy configured!"

echo "========================================="
echo "🎉 MeshØ Unified Platform Deployed! "
if [ -n "$DOMAIN" ]; then
    echo "Secure URL: https://$DOMAIN"
else
    echo "URL: http://$SERVER_IP"
fi
echo "========================================="
