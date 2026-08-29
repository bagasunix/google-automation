#!/bin/bash
# Install systemd services on VPS
# Usage: sudo bash scripts/install_services.sh

set -e

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
CURRENT_USER="$(whoami)"

echo "=== Installing Google Automation systemd Services ==="
echo "Project Path: $PROJECT_DIR"
echo "Running User: $CURRENT_USER"

# Replace paths with actual project directory
sed "s|/root/Project/google-automation|$PROJECT_DIR|g" scripts/systemd/google-automation.service | \
sed "s|User=root|User=$CURRENT_USER|g" | \
sudo tee /etc/systemd/system/google-automation.service > /dev/null

sed "s|/root/Project/google-automation|$PROJECT_DIR|g" scripts/systemd/google-dashboard.service | \
sed "s|User=root|User=$CURRENT_USER|g" | \
sudo tee /etc/systemd/system/google-dashboard.service > /dev/null

sudo systemctl daemon-reload
sudo systemctl enable google-automation.service
sudo systemctl enable google-dashboard.service

echo ""
echo "=== Services Installed & Enabled! ==="
echo "To start automation: sudo systemctl start google-automation"
echo "To start dashboard:  sudo systemctl start google-dashboard"
echo "To view logs:        sudo journalctl -u google-automation -f"
