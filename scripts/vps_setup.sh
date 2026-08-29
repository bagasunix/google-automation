#!/bin/bash
# VPS Setup Script for Google Automation on Ubuntu / Debian
# Run once on your VPS: bash scripts/vps_setup.sh

set -e

echo "=== Google Automation VPS Setup ==="

# 1. Update system packages
sudo apt update && sudo apt install -y \
    curl wget git build-essential \
    python3 python3-venv python3-pip \
    libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 \
    libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 \
    libasound2 libpango-1.0-0 libpangocairo-1.0-0 \
    xvfb unzip

# 2. Install Google Chrome Stable (if not installed)
if ! command -v google-chrome &>/dev/null; then
    echo "Installing Google Chrome..."
    wget -q -O - https://dl-ssl.google.com/linux/linux_signing_key.pub | sudo gpg --dearmor -o /etc/apt/trusted.gpg.d/google.gpg
    echo "deb [arch=amd64] http://dl.google.com/linux/chrome/deb/ stable main" | sudo tee /etc/apt/sources.list.d/google-chrome.list
    sudo apt update
    sudo apt install -y google-chrome-stable
fi

# 3. Setup Python Virtual Environment
echo "Setting up Python environment..."
cd worker
python3 -m venv .venv
source .venv/bin/activate
pip install --upgrade pip
pip install -r requirements.txt
python -m seleniumbase install chromedriver
cd ..

# 4. Build Go Binaries
echo "Building Go orchestrator & dashboard..."
if command -v go &>/dev/null; then
    go build -o bin/orchestrator cmd/main.go
    go build -o bin/dashboard cmd/dashboard/main.go
    echo "Go binaries built in ./bin"
fi

echo "=== Setup Completed! ==="
echo "To start: ./scripts/run.sh"
echo "To run dashboard server: ./bin/dashboard --serve :8080"
